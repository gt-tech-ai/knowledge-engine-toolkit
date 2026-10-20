// Package process builds the Supervisor from config: the stub (the default; starts
// nothing) or real operating-system processes (exec). A supervisor is not run through
// the client tier: a process's lifetime is not a call that a timeout or a retry can
// wrap; the readiness wait and the stop deadline are its own.
package process

import (
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/process/exec"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/process/stub"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Kind selects a supervisor.
type Kind string

const (
	// KindStub starts nothing (the default).
	KindStub Kind = "stub"

	// KindExec starts real processes.
	KindExec Kind = "exec"
)

// Config selects the supervisor.
type Config struct {
	// Kind selects the implementation; empty means KindStub.
	Kind Kind `yaml:"kind" mapstructure:"kind"`
}

// NewFromConfig returns the supervisor cfg.Kind selects; an unknown kind is
// CodeInvalidInput. It starts nothing.
func NewFromConfig(cfg Config) (interfaces.Supervisor, error) {
	switch cfg.Kind {
	case "", KindStub:
		return stub.Supervisor{}, nil
	case KindExec:
		return exec.New(), nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			"process: unknown kind "+string(cfg.Kind),
		)
	}
}
