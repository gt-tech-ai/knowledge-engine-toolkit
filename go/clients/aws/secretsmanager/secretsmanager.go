// Package secretsmanager builds the AWS Secrets Manager client from config: the stub (the
// default) or the AWS SDK client over an injected aws.Config (sdk), run through the
// client tier (Decorate).
package secretsmanager

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// API is the AWS Secrets Manager calls the toolkit's consumers make, with the SDK
// client's own signatures so the SDK client satisfies it unchanged.
//
// SDK seam — wraps the aws-sdk-go-v2 AWS Secrets Manager client; unit tests inject a
// generated mock.
type API interface {
	// GetSecretValue returns a secret's current value.
	GetSecretValue(
		ctx context.Context,
		in *secretsmanager.GetSecretValueInput,
		optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.GetSecretValueOutput, error)

	// DescribeSecret returns a secret's metadata.
	DescribeSecret(
		ctx context.Context,
		in *secretsmanager.DescribeSecretInput,
		optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.DescribeSecretOutput, error)

	// PutSecretValue stores a new version of a secret's value.
	PutSecretValue(
		ctx context.Context,
		in *secretsmanager.PutSecretValueInput,
		optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.PutSecretValueOutput, error)

	// ListSecrets lists secrets' metadata, one page at a time.
	ListSecrets(
		ctx context.Context,
		in *secretsmanager.ListSecretsInput,
		optFns ...func(*secretsmanager.Options),
	) (*secretsmanager.ListSecretsOutput, error)
}

const (
	// KindStub makes no AWS call (the default).
	KindStub client.Kind = "stub"

	// KindSDK calls AWS through the SDK client.
	KindSDK client.Kind = "sdk"
)

// Config selects the client.
type Config struct {
	// Kind selects the backend; empty means KindStub.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("secretsmanager").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the client's collaborators.
type Deps struct {
	// Decorations decorates the client and supplies its observability.
	decorate.Decorations

	// API replaces the SDK client KindSDK builds from AWS.
	API API

	// AWS is the SDK configuration (region, credentials) KindSDK builds its client
	// from when API is nil.
	AWS *aws.Config
}

// kinds is the backend table.
func kinds() client.Kinds[Config, Deps, API] {
	return client.Kinds[Config, Deps, API]{
		Family:   "secretsmanager",
		Default:  KindStub,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, API]{
			KindStub: func(context.Context, Config, Deps) (API, error) { return Stub{}, nil },
			KindSDK:  newSDK,
		},
	}
}

// newSDK returns Deps.API, else the SDK client over Deps.AWS; with neither it is
// CodeInvalidInput.
func newSDK(_ context.Context, _ Config, deps Deps) (API, error) {
	if deps.API != nil {
		return deps.API, nil
	}
	if deps.AWS == nil {
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"secretsmanager sdk: no aws config",
		)
	}
	return secretsmanager.NewFromConfig(*deps.AWS), nil
}

// NewFromConfig builds the client cfg.Kind selects and runs it through the client
// tier; an unknown kind is CodeInvalidInput. It makes no AWS call.
func NewFromConfig(ctx context.Context, cfg Config, deps Deps) (API, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

// Decorate runs every call of base through the client tier as the client named
// name. PutSecretValue runs once, never retried, because a replay may repeat its effect.
func Decorate(
	base API,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (API, error) {
	b, err := client.NewBase[API](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	return &decorator{base: base, Base: b}, nil
}

// decorator runs each call through the client tier.
type decorator struct {
	// base is the wrapped client.
	base API

	// Base runs each call through the client tier.
	client.Base
}

// Compile-time assertion that decorator satisfies API.
var _ API = (*decorator)(nil)

// GetSecretValue runs the call.
func (d *decorator) GetSecretValue(
	ctx context.Context,
	in *secretsmanager.GetSecretValueInput,
	optFns ...func(*secretsmanager.Options),
) (*secretsmanager.GetSecretValueOutput, error) {
	return client.Call(
		ctx,
		d.Base,
		"get_secret_value",
		d.base.GetSecretValue,
		in,
		optFns...)
}

// DescribeSecret runs the call.
func (d *decorator) DescribeSecret(
	ctx context.Context,
	in *secretsmanager.DescribeSecretInput,
	optFns ...func(*secretsmanager.Options),
) (*secretsmanager.DescribeSecretOutput, error) {
	return client.Call(
		ctx,
		d.Base,
		"describe_secret",
		d.base.DescribeSecret,
		in,
		optFns...)
}

// PutSecretValue runs the call once: it is not safe to replay.
func (d *decorator) PutSecretValue(
	ctx context.Context,
	in *secretsmanager.PutSecretValueInput,
	optFns ...func(*secretsmanager.Options),
) (*secretsmanager.PutSecretValueOutput, error) {
	return client.CallOnce(
		ctx,
		d.Base,
		"put_secret_value",
		d.base.PutSecretValue,
		in,
		optFns...)
}

// ListSecrets runs the call.
func (d *decorator) ListSecrets(
	ctx context.Context,
	in *secretsmanager.ListSecretsInput,
	optFns ...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	return client.Call(ctx, d.Base, "list_secrets", d.base.ListSecrets, in, optFns...)
}

// Stub is the client that makes no AWS call.
type Stub struct{}

// Compile-time assertion that Stub satisfies API.
var _ API = Stub{}

// GetSecretValue finds no secret.
func (Stub) GetSecretValue(
	_ context.Context,
	in *secretsmanager.GetSecretValueInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.GetSecretValueOutput, error) {
	return nil, apperr.NotFound(
		"stub secrets manager has no secret " + aws.ToString(in.SecretId),
	)
}

// DescribeSecret finds no secret.
func (Stub) DescribeSecret(
	_ context.Context,
	in *secretsmanager.DescribeSecretInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.DescribeSecretOutput, error) {
	return nil, apperr.NotFound(
		"stub secrets manager has no secret " + aws.ToString(in.SecretId),
	)
}

// PutSecretValue accepts the value and stores nothing.
func (Stub) PutSecretValue(
	_ context.Context,
	in *secretsmanager.PutSecretValueInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.PutSecretValueOutput, error) {
	return &secretsmanager.PutSecretValueOutput{Name: in.SecretId}, nil
}

// ListSecrets lists no secrets.
func (Stub) ListSecrets(
	_ context.Context,
	_ *secretsmanager.ListSecretsInput,
	_ ...func(*secretsmanager.Options),
) (*secretsmanager.ListSecretsOutput, error) {
	return &secretsmanager.ListSecretsOutput{}, nil
}
