// Package prompt builds the Prompter from config: deny (the default), which refuses every
// question for a front-end with no operator, or tty, which asks an operator through
// injected streams, so a capability never reads process stdin.
//
// A prompt waits on a human, so the client is built with Kinds.New and never run through
// the client tier: a decorated timeout would cancel the question and a retry would ask it
// twice.
package prompt

import (
	"context"
	"io"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prompt/deny"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/prompt/tty"
	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

const (
	// KindDeny refuses every question with the --yes guidance (the default).
	KindDeny client.Kind = "deny"

	// KindTTY asks on Deps.Out and reads the answer from Deps.In.
	KindTTY client.Kind = "tty"
)

// Config selects the prompter.
type Config struct {
	// Kind selects the backend; empty means KindDeny.
	Kind client.Kind `yaml:"kind" mapstructure:"kind"`

	// Name labels the prompter; empty uses the family name ("prompt").
	Name string `yaml:"name" mapstructure:"name"`
}

// Deps carries the prompter's streams.
type Deps struct {
	// In is the reader the answer comes from (KindTTY).
	In io.Reader

	// Out is the writer the question goes to (KindTTY).
	Out io.Writer
}

// kinds is the prompter backend table.
func kinds() client.Kinds[Config, Deps, interfaces.Prompter] {
	return client.Kinds[Config, Deps, interfaces.Prompter]{
		Family:  "prompt",
		Default: KindDeny,
		Backends: map[client.Kind]client.Backend[Config, Deps, interfaces.Prompter]{
			KindDeny: func(context.Context, Config, Deps) (interfaces.Prompter, error) {
				return deny.New(), nil
			},
			KindTTY: newTTY,
		},
	}
}

// newTTY builds the tty backend; it needs both streams.
func newTTY(_ context.Context, _ Config, deps Deps) (interfaces.Prompter, error) {
	if deps.In == nil || deps.Out == nil {
		return nil, apperr.New(apperr.CodeInvalidInput,
			"prompt tty: requires an input reader and an output writer")
	}
	return tty.New(deps.In, deps.Out), nil
}

// NewFromConfig builds the prompter cfg.Kind selects, undecorated (see the package
// doc). An unknown kind, or tty without both streams, is CodeInvalidInput.
func NewFromConfig(
	ctx context.Context,
	cfg Config,
	deps Deps,
) (interfaces.Prompter, error) {
	return kinds().New(ctx, cfg.Kind, cfg, deps)
}
