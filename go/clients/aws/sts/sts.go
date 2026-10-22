// Package sts builds the AWS STS client from config: the stub (the default) or the
// AWS SDK client over an injected aws.Config (sdk), run through the client tier
// (Decorate).
package sts

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// API is the AWS STS calls the toolkit's consumers make, with the SDK client's own
// signatures so the SDK client satisfies it unchanged.
//
// SDK seam — wraps the aws-sdk-go-v2 AWS STS client; unit tests inject a generated mock.
type API interface {
	// GetCallerIdentity returns the account, ARN and user id of the calling identity.
	GetCallerIdentity(
		ctx context.Context,
		in *sts.GetCallerIdentityInput,
		optFns ...func(*sts.Options),
	) (*sts.GetCallerIdentityOutput, error)
}

// StubAccount is the account id the stub reports.
const StubAccount = "000000000000"

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
	// uses the family name ("sts").
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
		Family:   "sts",
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
		return nil, apperr.New(apperr.CodeInvalidInput, "sts sdk: no aws config")
	}
	return sts.NewFromConfig(*deps.AWS), nil
}

// NewFromConfig builds the client cfg.Kind selects and runs it through the client
// tier; an unknown kind is CodeInvalidInput. It makes no AWS call.
func NewFromConfig(ctx context.Context, cfg Config, deps Deps) (API, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}

// Decorate runs every call of base through the client tier as the client named
// name.
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

// GetCallerIdentity runs the call.
func (d *decorator) GetCallerIdentity(
	ctx context.Context,
	in *sts.GetCallerIdentityInput,
	optFns ...func(*sts.Options),
) (*sts.GetCallerIdentityOutput, error) {
	return client.Call(
		ctx,
		d.Base,
		"get_caller_identity",
		d.base.GetCallerIdentity,
		in,
		optFns...)
}

// Stub is the client that makes no AWS call.
type Stub struct{}

// Compile-time assertion that Stub satisfies API.
var _ API = Stub{}

// GetCallerIdentity returns the stub identity in StubAccount.
func (Stub) GetCallerIdentity(
	_ context.Context,
	_ *sts.GetCallerIdentityInput,
	_ ...func(*sts.Options),
) (*sts.GetCallerIdentityOutput, error) {
	return &sts.GetCallerIdentityOutput{
		Account: aws.String(StubAccount),
		Arn:     aws.String("arn:aws:iam::" + StubAccount + ":user/stub"),
		UserId:  aws.String("stub"),
	}, nil
}
