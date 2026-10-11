// Package s3 implements the S3 ConnectorSource for the connector family: a "test
// connection" probe that lists a bounded page under a connector's bucket/prefix using the
// connector's OWN credentials, over the shared StorageClient (INJECTED — this package
// never imports go/clients/storage, so there is no lateral same-layer import). Its
// credential strategies (STS AssumeRole, static access key) are the S3-specific,
// AWS-typed part of the family; an OAuth2-with-refresh strategy for SaaS connectors is a
// future sibling package.
package s3

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Auth method vocabulary (mirrors the connector's stored auth_method values).
const (
	// authMethodIAMRole assumes the connector's role via STS for credentials.
	authMethodIAMRole = "iam_role"
	// authMethodAccessKey uses a static access key/secret pair.
	authMethodAccessKey = "access_key"
)

// STS's constraint on AssumeRole's ExternalId: 2–1224 characters of [\w+=,.@:/-].
// Checking it at build turns a malformed id into CodeInvalidInput up front instead of an
// STS ValidationError inside the first S3 call. (RE2 caps a repeat count at 1000, so the
// length is checked separately.)
const (
	// minExternalIDLen is the shortest ExternalId STS accepts.
	minExternalIDLen = 2
	// maxExternalIDLen is the longest ExternalId STS accepts.
	maxExternalIDLen = 1224
)

// externalIDChars matches an ExternalId made only of the characters STS allows.
var externalIDChars = regexp.MustCompile(`^[\w+=,.@:/-]+$`)

// credentialsFromConfig selects the S3 credential strategy for method, failing loudly
// (CodeInvalidInput) on an unknown method (the foundation/logger NewFromConfig pattern)
// or on a config that cannot authenticate:
//   - access_key presents the static key and requires a non-empty key id and secret;
//   - iam_role assumes the connector's role via STS with the connector's ExternalId (lazy
//     — the provider retrieves on first use) and requires a role ARN, an STS client, and
//     an ExternalId within STS's format.
//
// The ExternalId only defeats the confused deputy if the caller's platform generates it
// per tenant and stores it with the connector; it must never be taken from the tenant's
// own input, or a tenant could present another tenant's role ARN together with that
// tenant's id.
//
// The returned provider marks every retrieval failure (see credentialsError) so the list
// classifier reports it as a configuration problem rather than an unreachable store.
func credentialsFromConfig(
	method, iamRoleARN, externalID, accessKeyID, secretAccessKey string,
	stsClient stscreds.AssumeRoleAPIClient,
) (aws.CredentialsProvider, error) {
	switch method {
	case authMethodAccessKey:
		if strings.TrimSpace(accessKeyID) == "" ||
			strings.TrimSpace(secretAccessKey) == "" {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"connector s3 auth: access_key requires an access key id and a secret access key",
			)
		}
		return markedProvider{inner: credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"",
		)}, nil
	case authMethodIAMRole:
		if err := validateIAMRole(iamRoleARN, externalID, stsClient); err != nil {
			return nil, err
		}
		return markedProvider{inner: stscreds.NewAssumeRoleProvider(
			stsClient,
			iamRoleARN,
			func(o *stscreds.AssumeRoleOptions) { o.ExternalID = aws.String(externalID) },
		)}, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("connector s3 auth: unknown method %q", method),
		)
	}
}

// validateIAMRole checks the iam_role inputs STS needs, returning a CodeInvalidInput
// error naming the first missing or malformed one.
func validateIAMRole(
	iamRoleARN, externalID string,
	stsClient stscreds.AssumeRoleAPIClient,
) error {
	switch {
	case strings.TrimSpace(iamRoleARN) == "":
		return apperr.New(
			apperr.CodeInvalidInput,
			"connector s3 auth: iam_role requires a role ARN",
		)
	case stsClient == nil:
		return apperr.New(
			apperr.CodeInvalidInput,
			"connector s3 auth: iam_role requires an STS client",
		)
	case externalID == "":
		return apperr.New(
			apperr.CodeInvalidInput,
			"connector s3 auth: iam_role requires an external id",
		)
	case len(externalID) < minExternalIDLen, len(externalID) > maxExternalIDLen,
		!externalIDChars.MatchString(externalID):
		return apperr.New(
			apperr.CodeInvalidInput,
			"connector s3 auth: external id must be 2-1224 characters of [A-Za-z0-9_+=,.@:/-]",
		)
	}
	return nil
}

// markedProvider wraps a credentials provider so a retrieval failure carries a
// credentialsError. The SDK resolves credentials lazily inside the first S3 call, so
// without the mark a refused AssumeRole (STS's AccessDenied is unmodeled, with an unknown
// fault) is indistinguishable from an unreachable store.
type markedProvider struct {
	// inner is the strategy's own provider (static or STS AssumeRole).
	inner aws.CredentialsProvider
}

// Retrieve returns inner's credentials, marking a failure as a credentialsError unless it
// is the caller's own cancellation or deadline (that is not a credentials problem).
func (p markedProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	creds, err := p.inner.Retrieve(ctx)
	if err == nil {
		return creds, nil
	}
	if apperr.StdIs(err, context.Canceled) ||
		apperr.StdIs(err, context.DeadlineExceeded) {
		return creds, apperr.Wrap(
			err,
			apperr.ContextCode(err, apperr.CodeCanceled),
			"retrieve credentials",
		)
	}
	return creds, &credentialsError{err: err}
}

// credentialsError marks a failure to retrieve the connector's credentials (a refused or
// misconfigured AssumeRole, rejected static keys). classifyListError maps it to
// CodeInvalidInput.
type credentialsError struct {
	// err is the provider's underlying failure.
	err error
}

// Error describes the failure with its cause.
func (e *credentialsError) Error() string {
	return "connector s3: retrieve credentials: " + e.err.Error()
}

// Unwrap exposes the provider's underlying failure to errors.Is/As.
func (e *credentialsError) Unwrap() error { return e.err }
