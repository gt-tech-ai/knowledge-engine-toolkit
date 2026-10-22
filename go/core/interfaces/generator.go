package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Generator derives files from a codebase's sources: a contract's code, a config
// reference, a lock file. Its outputs are committed, so a drift check can compare a
// fresh render with what the codebase holds.
type Generator interface {
	// Name identifies the generator.
	Name() string

	// Inputs selects the source files the outputs derive from; a change outside them
	// cannot cause drift.
	Inputs() types.FileSelector

	// Render produces the outputs from cb.
	Render(ctx context.Context, cb Codebase) (types.Artifacts, error)
}
