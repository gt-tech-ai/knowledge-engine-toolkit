package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ProcedureRunner runs procedures by name.
type ProcedureRunner interface {
	// Run runs the procedure named name with input as its variables, returning one
	// result per executed step. A failed step is a failing result, not an error; an
	// unknown procedure, a missing variable or a refused confirmation is an error.
	Run(
		ctx context.Context,
		name string,
		input map[string]any,
		opts types.RunOptions,
	) (types.StepResults, error)
}
