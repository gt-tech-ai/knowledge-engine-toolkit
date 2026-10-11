// Package secrets is the credential-source client tier: it selects a secret backend —
// an environment variable (env/), a git-ignored local file (file/), or AWS Secrets
// Manager (awssm/) — by Kind and returns the interfaces.Source contract, so switching
// where a credential is read is a configuration change, not a caller edit. The contract
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
	"fmt"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/awssm"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/env"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/secrets/file"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Kind selects the credential backend.
type Kind int

const (
	// KindEnv reads credentials from environment variables (read-only).
	KindEnv Kind = iota
	// KindFile reads and writes credentials in a git-ignored local file.
	KindFile
	// KindAWSSM reads credentials from AWS Secrets Manager (read-only; IaC writes them).
	KindAWSSM
)

// String returns the string form of Kind.
func (k Kind) String() string {
	switch k {
	case KindEnv:
		return "env"
	case KindFile:
		return "file"
	case KindAWSSM:
		return "aws-sm"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// Config selects and configures the credential backend. Only the fields for the
// selected Kind are consulted; each map is the caller-supplied Ref→address policy.
type Config struct {
	// EnvVars maps a Ref to its environment variable name (KindEnv).
	EnvVars map[types.Ref]string

	// Files maps a Ref to its git-ignored file location (KindFile).
	Files map[types.Ref]file.Location

	// Secrets maps a Ref to its Secrets Manager location (KindAWSSM).
	Secrets map[types.Ref]awssm.Location

	// Runner runs `git check-ignore` for the file backend's write guard (KindFile).
	Runner interfaces.CommandRunner

	// Region is the AWS region for the Secrets Manager backend (KindAWSSM).
	Region string

	// Kind selects the backend. The zero value is KindEnv.
	Kind Kind
}

// NewFromConfig builds the credential Source selected by cfg.Kind: environment variables
// (KindEnv), a git-ignored file (KindFile), or AWS Secrets Manager (KindAWSSM). It is
// the app-wiring entrypoint and fails loudly on an unknown Kind or a Kind whose required
// dependency (the file backend's CommandRunner) is missing.
func NewFromConfig(ctx context.Context, cfg Config) (interfaces.Source, error) {
	switch cfg.Kind {
	case KindEnv:
		return env.New(cfg.EnvVars), nil
	case KindFile:
		if cfg.Runner == nil {
			return nil, apperr.New(
				apperr.CodeInvalidInput,
				"file credential source requires a CommandRunner for the git-ignore write guard",
			)
		}
		return file.New(cfg.Files, cfg.Runner), nil
	case KindAWSSM:
		return awssm.NewClient(ctx, cfg.Region, cfg.Secrets)
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown secrets kind: %v", cfg.Kind),
		)
	}
}
