// Package secrets is the credential-source client tier: it selects a secret backend —
// an environment variable (env/), a git-ignored local file (file/), or AWS Secrets
// Manager (awssm/) — by Kind (env when empty) and returns the interfaces.Source
// contract, so switching where a credential is read is a configuration change, not a
// caller edit. The contract
// (interfaces.Source plus the opaque types.Secret) lives in core; each backend
// implements it; this is the factory, mirroring the cache/storage/auth NewFromConfig
// pattern.
//
// This tier is the generic MECHANISM. The Ref→backend-address mapping is supplied by
// the caller as POLICY — this tier knows nothing of any specific credential's meaning,
// and routing a Ref to a particular Kind is likewise the caller's concern.
package secrets

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/awssm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/env"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/file"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

const (
	// KindEnv reads credentials from environment variables (read-only; the default).
	KindEnv client.Kind = "env"

	// KindFile reads and writes credentials in a git-ignored local file.
	KindFile client.Kind = "file"

	// KindAWSSM reads credentials from AWS Secrets Manager (read-only; IaC writes them).
	KindAWSSM client.Kind = "aws-sm"
)

// Config selects the credential backend and maps each Ref to its address there.
type Config struct {
	// Kind selects the backend; empty means KindEnv.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// EnvVars maps a Ref to its environment variable name (KindEnv).
	EnvVars map[types.Ref]string `yaml:"env_vars" mapstructure:"env_vars"`

	// Files maps a Ref to its git-ignored file location (KindFile).
	Files map[types.Ref]file.Location `yaml:"files" mapstructure:"files"`

	// Secrets maps a Ref to its Secrets Manager location (KindAWSSM).
	Secrets map[types.Ref]awssm.Location `yaml:"secrets" mapstructure:"secrets"`

	// Region is the AWS region for the Secrets Manager backend (KindAWSSM).
	Region string `yaml:"region" mapstructure:"region"`

	// Name labels the client in decoration overrides, metrics, spans and logs; empty
	// uses the family name ("secrets").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the credential backends' runtime collaborators.
type Deps struct {
	// Decorations decorates the client from the decoration config (decoration.clients)
	// and supplies its observability.
	decorate.Decorations

	// Runner runs `git check-ignore` for the file backend's write guard (KindFile).
	Runner interfaces.CommandRunner
}

// kinds is the secrets backend table.
func kinds() client.Kinds[Config, Deps, interfaces.Source] {
	return client.Kinds[Config, Deps, interfaces.Source]{
		Family:   "secrets",
		Default:  KindEnv,
		Decorate: Decorate,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.Source]{
			KindEnv: func(_ context.Context, cfg Config, _ Deps) (interfaces.Source, error) {
				return env.New(cfg.EnvVars), nil
			},
			KindFile: func(_ context.Context, cfg Config, deps Deps) (interfaces.Source, error) {
				if deps.Runner == nil {
					return nil, apperr.New(
						apperr.CodeInvalidInput,
						"file credential source requires a CommandRunner for the git-ignore write guard",
					)
				}
				return file.New(cfg.Files, deps.Runner), nil
			},
			KindAWSSM: func(ctx context.Context, cfg Config, _ Deps) (interfaces.Source, error) {
				return awssm.NewClient(ctx, cfg.Region, cfg.Secrets)
			},
		},
	}
}

// NewFromConfig builds the credential source cfg.Kind selects, run through the client
// tier. ctx bounds the AWS
// config load of KindAWSSM. An unknown kind, or KindFile without a Runner, is
// CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.Source, error) {
	return kinds().Build(ctx, cfg.Kind, cfg, deps, deps.Decorations, cfg.Name)
}
