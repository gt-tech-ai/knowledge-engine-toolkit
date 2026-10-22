// Package iam builds the AWS IAM client from config: the stub (the default) or the
// AWS SDK client over an injected aws.Config (sdk), run through the client tier
// (Decorate).
package iam

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// API is the AWS IAM calls the toolkit's consumers make, with the SDK client's own
// signatures so the SDK client satisfies it unchanged.
//
// SDK seam — wraps the aws-sdk-go-v2 AWS IAM client; unit tests inject a generated mock.
type API interface {
	// SimulatePrincipalPolicy evaluates whether a principal is allowed the given actions.
	SimulatePrincipalPolicy(
		ctx context.Context,
		in *iam.SimulatePrincipalPolicyInput,
		optFns ...func(*iam.Options),
	) (*iam.SimulatePrincipalPolicyOutput, error)
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
	// uses the family name ("iam").
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
		Family:   "iam",
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
		return nil, apperr.New(apperr.CodeInvalidInput, "iam sdk: no aws config")
	}
	return iam.NewFromConfig(*deps.AWS), nil
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

// SimulatePrincipalPolicy runs the call.
func (d *decorator) SimulatePrincipalPolicy(
	ctx context.Context,
	in *iam.SimulatePrincipalPolicyInput,
	optFns ...func(*iam.Options),
) (*iam.SimulatePrincipalPolicyOutput, error) {
	return client.Call(
		ctx,
		d.Base,
		"simulate_principal_policy",
		d.base.SimulatePrincipalPolicy,
		in,
		optFns...)
}

// Stub is the client that makes no AWS call.
type Stub struct{}

// Compile-time assertion that Stub satisfies API.
var _ API = Stub{}

// SimulatePrincipalPolicy evaluates nothing.
func (Stub) SimulatePrincipalPolicy(
	_ context.Context,
	_ *iam.SimulatePrincipalPolicyInput,
	_ ...func(*iam.Options),
) (*iam.SimulatePrincipalPolicyOutput, error) {
	return &iam.SimulatePrincipalPolicyOutput{}, nil
}
