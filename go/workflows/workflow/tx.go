package workflow

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// NewTxWorkflow returns a workflow that runs pipe inside one transaction opened
// by txMgr: the pipeline sees the transaction's context, so every repository call
// it makes joins the transaction, which commits when the pipeline succeeds and
// rolls back when it fails. The pipeline's error, or the commit's, is returned
// unchanged (its code intact) with a zero output — a result whose transaction
// did not commit never reaches the caller.
func NewTxWorkflow[In, Out any](
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
