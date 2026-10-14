// Package workflow is the workflow tier: a Base builds a consumer's request workflows,
// the one boundary observed in every environment and the owner of the transaction
// boundary. Every workflow is a core interfaces.Workflow, decorated in the tier's order
// (Tier), so a consumer declares no workflow interface of its own.
package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Tier is the workflow tier's decoration: recovery → logging → tracing → metrics →
// timeout, outermost first.
func Tier() decorate.Tier {
	return decorate.Tier{Name: "workflow", Order: []decorate.Layer{
		decorate.LayerRecovery,
		decorate.LayerLogging,
		decorate.LayerTracing,
		decorate.LayerMetrics,
		decorate.LayerTimeout,
	}}
}

// Compile-time interface compliance check.
var _ interfaces.Workflow[string, string] = flow[string, string](nil)

// flow is a workflow body: a function that is an interfaces.Workflow.
type flow[In, Out any] func(context.Context, In) (Out, error)

// Execute runs the workflow body.
func (f flow[In, Out]) Execute(ctx context.Context, input In) (Out, error) {
	return f(ctx, input)
}

// Base builds a consumer's request workflows over the workflow tier's collaborators.
type Base struct {
	// tx opens the transaction a Tx workflow runs in.
	tx interfaces.TransactionManager

	// deps backs every workflow's decoration.
	deps decorate.Deps
}

// NewBase builds the Base over tx (needed only by Tx) whose workflows are decorated
// with deps.
func NewBase(tx interfaces.TransactionManager, deps decorate.Deps) Base {
	return Base{tx: tx, deps: deps}
}

// Func wraps fn as the decorated workflow named op; it runs as op's "Execute"
// operation. A nil fn panics, as a wiring bug.
func (b Base) Func[In, Out any](
	op string,
	fn func(context.Context, In) (Out, error),
) interfaces.Workflow[In, Out] {
	if fn == nil {
		panic("workflow: fn must not be nil")
	}
	subject := Tier().Subject(op, b.deps)
	return flow[In, Out](func(ctx context.Context, input In) (Out, error) {
		return subject.Exec(ctx, "Execute", func(ctx context.Context) (Out, error) {
			return fn(ctx, input)
		})
	})
}

// Wrap decorates a custom workflow (a saga, a multi-transaction flow) as op.
func (b Base) Wrap[In, Out any](
	op string,
	wf interfaces.Workflow[In, Out],
) interfaces.Workflow[In, Out] {
	return b.Func(op, wf.Execute)
}

// Read runs pipe as the decorated workflow op, with no transaction.
func (b Base) Read[In, Out any](
	op string,
	pipe interfaces.Pipeline[In, Out],
) interfaces.Workflow[In, Out] {
	return b.Func(op, pipe.Execute)
}

// Tx runs pipe in one transaction as the decorated workflow op: the pipeline sees the
// transaction's context, so every repository call it makes joins the transaction,
// which commits when the pipeline succeeds and rolls back when it fails. The
// pipeline's error, or the commit's, is returned with its code intact and a zero
// output, so a result whose transaction did not commit never reaches the caller.
func (b Base) Tx[In, Out any](
	op string,
	pipe interfaces.Pipeline[In, Out],
) interfaces.Workflow[In, Out] {
	return b.Func(op, func(ctx context.Context, in In) (Out, error) {
		var out Out
		err := b.tx.WithTransaction(ctx, func(txCtx context.Context) error {
			var err error
			out, err = pipe.Execute(txCtx, in)
			return err
		})
		if err != nil {
			var zero Out
			return zero, err
		}
		return out, nil
	})
}
