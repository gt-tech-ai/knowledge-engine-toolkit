// Package awssm implements the AWS Secrets Manager credential Source: it reads a
// credential from a Secrets Manager secret (a JSON object) named by a caller-supplied
// Ref→location mapping, extracting the configured JSON field. It is a read-only backend
// of clients/secrets (KindAWSSM) — Terraform/IaC writes the secrets out-of-band, so Put
// is unsupported. The AWS SDK is reached through the exported API seam so unit tests
// drive a generated mock without network access.
package awssm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// SecretsManagerAPI is the subset of the aws-sdk-go-v2 Secrets Manager client used by the
// source. It is an exported, injectable seam so unit tests drive it with a generated mock
// without network access, mirroring the S3 client's S3API seam. *secretsmanager.Client
// satisfies it. The name is distinct (not a bare API) so its generated mock does not
// collide with the SQS client's API seam in the shared mocks package.
//
// SDK seam — mocks the AWS Secrets Manager SDK client
// (aws-sdk-go-v2/service/secretsmanager), cannot compose with a core port.
type SecretsManagerAPI interface {
	// GetSecretValue retrieves a secret's value (the SecretString JSON blob).
	GetSecretValue(
		context.Context,
		*secretsmanager.GetSecretValueInput,
		...func(*secretsmanager.Options),
	) (*secretsmanager.GetSecretValueOutput, error)
}

// Location addresses a credential within Secrets Manager: the secret's id and the JSON
// field within its SecretString value.
type Location struct {
	// SecretID is the Secrets Manager secret name or ARN.
	SecretID string
	// Field is the key within the secret's JSON object holding the credential value.
	Field string
}

// source reads credentials from Secrets Manager secrets named by a caller-supplied
// Ref→location mapping, through the injectable API seam.
type source struct {
	// api is the Secrets Manager client seam (real client or a mock in tests).
	api SecretsManagerAPI
	// locs maps a credential Ref to the secret id + JSON field that holds its value.
	locs map[types.Ref]Location
}

// New builds a Secrets Manager Source over the given API seam and Ref→location mapping.
// It is the seam-injected constructor used by tests and advanced wiring; NewClient
// builds the real AWS-backed Source.
func New(api SecretsManagerAPI, locs map[types.Ref]Location) interfaces.Source {
	return source{api: api, locs: locs}
}

// NewClient builds a Secrets Manager Source using the default AWS credential chain (IRSA
// in cluster, the shared credential file / env locally) for the given region.
func NewClient(
	ctx context.Context,
	region string,
	locs map[types.Ref]Location,
) (interfaces.Source, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"loading aws config for secrets manager",
		)
	}
	return New(secretsmanager.NewFromConfig(awsCfg), locs), nil
}

// Get resolves ref to its secret + field, fetches the secret's JSON value, and returns
// the field as an opaque Secret. An unmapped ref, a missing secret string, or a
// missing/empty field is a coded not-found error; an SDK failure is a coded upstream
// error.
func (s source) Get(ctx context.Context, ref types.Ref) (types.Secret, error) {
	loc, ok := s.locs[ref]
	if !ok {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("no secrets-manager location mapped for credential %s", ref),
		)
	}
	out, err := s.api.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(loc.SecretID),
	})
	if err != nil {
		return types.Secret{}, apperr.Wrap(
			err,
			apperr.CodeUpstream,
			"fetching secret from secrets manager",
		)
	}
	if out.SecretString == nil {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf("secret %q has no string value for %s", loc.SecretID, ref),
		)
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(*out.SecretString), &fields); err != nil {
		return types.Secret{}, apperr.Wrap(
			err,
			apperr.CodeInternal,
			"decoding secrets manager json",
		)
	}
	value, found := fields[loc.Field]
	if !found || value == "" {
		return types.Secret{}, apperr.NotFound(
			fmt.Sprintf(
				"field %q not found in secret %q for %s",
				loc.Field,
				loc.SecretID,
				ref,
			),
		)
	}
	return types.NewSecret(value), nil
}

// Put is unsupported: Secrets Manager secrets are written out-of-band by IaC. It returns
// a coded error so a caller routing a write to this backend fails loudly.
func (s source) Put(_ context.Context, ref types.Ref, _ types.Secret) error {
	return apperr.New(
		apperr.CodeInvalidInput,
		fmt.Sprintf(
			"aws-sm credential source is read-only (IaC writes secrets): cannot write %s",
			ref,
		),
	)
}
