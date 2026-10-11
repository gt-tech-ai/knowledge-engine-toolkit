package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow/decorators"
)

// TestNewTxWorkflow_RunsPipelineInOneTransaction tests that the transactional
// workflow runs its pipeline inside exactly one transaction.
//
// Why this test is important:
//   - A command workflow's writes must commit or roll back together; a pipeline
//     run outside the transaction would leave partial writes behind on failure
//
// What it tests:
//   - WithTransaction is called once, the pipeline sees the transaction's
//     context (InTx is true) and the input, and the pipeline's output is returned
func TestNewTxWorkflow_RunsPipelineInOneTransaction(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	txMgr := mocks.NewMockTransactionManager(ctrl)
	pipe := mocks.NewMockPipeline[string, int](ctrl)

	txMgr.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Times(1).DoAndReturn(
		func(ctx context.Context, fn func(context.Context) error) error {
			return fn(interfaces.WithInTx(ctx))
		})
	pipe.EXPECT().Execute(gomock.Any(), "order-7").Times(1).DoAndReturn(
		func(ctx context.Context, _ string) (int, error) {
			if !interfaces.InTx(ctx) {
				return 0, apperr.New(
					apperr.CodeInternal,
					"pipeline ran outside the transaction",
				)
			}
			return 42, nil
		})

	out, err := workflow.NewTxWorkflow[string, int](
		txMgr,
		pipe,
	).Execute(context.Background(), "order-7")

	require.NoError(t, err)
	assert.Equal(t, 42, out)
}

// TestNewTxWorkflow_PropagatesCodedError tests that the transactional workflow
// returns the coded error of a failed pipeline or a failed commit.
//
// Why this test is important:
//   - The transport maps the error code to a status; losing it would turn a
//     conflict or an outage into a generic internal error
//   - A result produced by a rolled-back transaction must not leak to the caller
//
// What it tests:
//   - A pipeline error coded CONFLICT comes back with that code and a zero output
//   - A commit error coded UNAVAILABLE after a successful pipeline comes back
//     with that code and a zero output
func TestNewTxWorkflow_PropagatesCodedError(t *testing.T) {
	t.Parallel()

	t.Run("pipeline error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		txMgr := mocks.NewMockTransactionManager(ctrl)
		pipe := mocks.NewMockPipeline[string, int](ctrl)
		txMgr.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) })
		pipe.EXPECT().
			Execute(gomock.Any(), "dup").
			Return(7, apperr.New(apperr.CodeConflict, "duplicate"))

		out, err := workflow.NewTxWorkflow[string, int](
			txMgr,
			pipe,
		).Execute(context.Background(), "dup")

		require.Error(t, err)
		assert.Equal(t, apperr.CodeConflict, apperr.Code(err))
		assert.Equal(t, 0, out)
	})

	t.Run("commit error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		txMgr := mocks.NewMockTransactionManager(ctrl)
		pipe := mocks.NewMockPipeline[string, int](ctrl)
		txMgr.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).DoAndReturn(
			func(ctx context.Context, fn func(context.Context) error) error {
				if err := fn(ctx); err != nil {
					return err
				}
				return apperr.New(apperr.CodeUnavailable, "commit failed")
			})
		pipe.EXPECT().Execute(gomock.Any(), "ok").Return(9, nil)

		out, err := workflow.NewTxWorkflow[string, int](
			txMgr,
			pipe,
		).Execute(context.Background(), "ok")

		require.Error(t, err)
		assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
		assert.Equal(t, 0, out)
	})
}

// TestDecorate_AppliesTimeoutAndRecovery tests that the standard decorate helper
// applies the configured timeout and the panic recovery.
//
// Why this test is important:
//   - Every workflow is wrapped through this one helper; a dropped timeout lets a
//     hung dependency hold a request forever, and a dropped recovery lets one
//     panic crash the serving process
//
// What it tests:
//   - With cfg.Timeout 5ms a workflow that waits on its context returns
//     context.DeadlineExceeded
//   - With cfg.Timeout 0 the workflow's context carries no deadline and its
//     output passes through unchanged
//   - A panicking workflow returns an INTERNAL error reading "panic recovered: boom"
func TestDecorate_AppliesTimeoutAndRecovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	slow := workflow.NewBaseWorkflow(func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	_, err := decorators.Decorate[string, string](
		slow,
		"slow.op",
		fixtures.NopLogger(),
		nil,
		nil,
		workflowscfg.Config{Timeout: 5 * time.Millisecond},
	).Execute(ctx, "in")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	plain := workflow.NewBaseWorkflow(
		func(ctx context.Context, in string) (string, error) {
			if _, ok := ctx.Deadline(); ok {
				return "", apperr.New(apperr.CodeInternal, "unexpected deadline")
			}
			return in + "-done", nil
		},
	)
	out, err := decorators.Decorate[string, string](plain, "plain.op", nil, nil, nil,
		workflowscfg.DefaultConfig()).Execute(ctx, "in")
	require.NoError(t, err)
	assert.Equal(t, "in-done", out)

	panicky := workflow.NewBaseWorkflow(func(context.Context, string) (string, error) {
		panic("boom")
	})
	_, err = decorators.Decorate[string, string](panicky, "panic.op", nil, nil, nil,
		workflowscfg.DefaultConfig()).Execute(ctx, "in")
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	assert.Contains(t, err.Error(), "panic recovered: boom")
}
