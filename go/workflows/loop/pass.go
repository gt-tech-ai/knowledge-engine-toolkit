package loop

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// pass is one loop iteration as a workflow body.
type pass func(ctx context.Context) error

// Execute runs the iteration.
func (p pass) Execute(ctx context.Context, _ struct{}) (struct{}, error) {
	return struct{}{}, p(ctx)
}

// Pass adapts fn, one iteration of a loop, into the workflow a Ticker runs, so the
// composition root can decorate it like any other workflow. A nil fn panics.
func Pass(fn func(ctx context.Context) error) interfaces.Workflow[struct{}, struct{}] {
	if fn == nil {
		panic("loop: pass fn must not be nil")
	}
	return pass(fn)
}
