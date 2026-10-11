package pipeline

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Then composes two pipelines into one: first's output is second's input. An error from
// first is returned unchanged and second does not run. Each stage keeps its own
// decorators, so building each with decorators.NewBuilder before composing gives one
// span and one metric series per stage. A nil stage panics, as a wiring bug.
func Then[A, B, C any](
	first interfaces.Pipeline[A, B],
	second interfaces.Pipeline[B, C],
) interfaces.Pipeline[A, C] {
	if first == nil || second == nil {
		panic("pipeline: Then stages must not be nil")
	}
	return NewBasePipeline(func(ctx context.Context, input A) (C, error) {
		mid, err := first.Execute(ctx, input)
		if err != nil {
			var zero C
			return zero, err
		}
		return second.Execute(ctx, mid)
	})
}
