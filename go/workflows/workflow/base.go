package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow/decorators"
)

// Base builds a consumer's request workflows: the one decorated boundary per request
// (logging, metrics, tracing, per-op timeout, recovery) and the owner of the
// transaction boundary. Every workflow it builds is an interfaces.Workflow, so a
// consumer declares no workflow interface of its own.
type Base struct {
	// tx opens the transaction a Tx workflow runs in.
	tx interfaces.TransactionManager

	// obs is the workflow tier's observability.
	obs decorate.Observability

	// cfg tunes the workflow decorators (cfg.Timeout bounds each request; 0 = none).
	cfg workflowscfg.Config
}

// NewBase builds the Base over tx (needed only by Tx), the workflow tier's obs and cfg.
func NewBase(
	tx interfaces.TransactionManager,
	obs decorate.Observability,
	cfg workflowscfg.Config,
) Base {
	return Base{tx: tx, obs: obs, cfg: cfg}
}

// Tx runs pipe in one transaction under the workflow decorator stack named op.
func (b Base) Tx[In, Out any](
	op string,
	pipe interfaces.Pipeline[In, Out],
) interfaces.Workflow[In, Out] {
	return b.Wrap(op, newTxWorkflow(b.tx, pipe))
}

// Read runs pipe under the workflow decorator stack named op, with no transaction.
func (b Base) Read[In, Out any](
	op string,
	pipe interfaces.Pipeline[In, Out],
) interfaces.Workflow[In, Out] {
	return b.Wrap(op, NewBaseWorkflow(pipe.Execute))
}

// Wrap puts a custom workflow (a saga, a multi-transaction flow) under the workflow
// decorator stack named op: logging, metrics, tracing, the cfg.Timeout deadline and
// panic recovery, in the Builder's documented order (recovery outermost). A nil
// collaborator leaves its layer out, and a zero timeout applies no deadline.
func (b Base) Wrap[In, Out any](
	op string,
	wf interfaces.Workflow[In, Out],
) interfaces.Workflow[In, Out] {
	return decorators.NewBuilder(wf, op).
		WithLogging(b.obs.Logger).
		WithMetrics(b.obs.Metrics).
		WithTracing(b.obs.Tracer).
		WithTimeout(b.cfg.Timeout).
		WithRecovery().
		Build()
}

// newTxWorkflow returns a workflow that runs pipe inside one transaction opened
// by txMgr: the pipeline sees the transaction's context, so every repository call
// it makes joins the transaction, which commits when the pipeline succeeds and
// rolls back when it fails. The pipeline's error, or the commit's, is returned
// unchanged (its code intact) with a zero output — a result whose transaction
// did not commit never reaches the caller.
func newTxWorkflow[In, Out any](
	txMgr interfaces.TransactionManager,
	pipe interfaces.Pipeline[In, Out],
) interfaces.Workflow[In, Out] {
	return NewBaseWorkflow(func(ctx context.Context, in In) (Out, error) {
		var out Out
		err := txMgr.WithTransaction(ctx, func(txCtx context.Context) error {
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
