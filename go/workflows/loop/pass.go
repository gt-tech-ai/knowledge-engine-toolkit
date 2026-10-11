package loop

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// Pass adapts fn, one iteration of a loop, into the workflow a Ticker runs, so the
// composition root can decorate it like any other workflow. A nil fn panics.
func Pass(fn func(ctx context.Context) error) interfaces.Workflow[struct{}, struct{}] {
	if fn == nil {
		panic("loop: pass fn must not be nil")
	}
	return workflow.NewBaseWorkflow(
		func(ctx context.Context, _ struct{}) (struct{}, error) {
			return struct{}{}, fn(ctx)
		},
	)
}
