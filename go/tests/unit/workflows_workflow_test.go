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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// TestWorkflowTier_Order tests the workflow tier's decorator order.
//
// Why this test is important:
//   - The order is the tier's documented contract (ARCHITECTURE.md#decorator-order):
//     the request workflow is the one boundary observed in every environment.
//
// What it tests:
//   - The tier is named "workflow" and orders recovery → logging → tracing → metrics →
//     timeout.
func TestWorkflowTier_Order(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "workflow", Order: []decorate.Layer{
		decorate.LayerRecovery, decorate.LayerLogging, decorate.LayerTracing,
		decorate.LayerMetrics, decorate.LayerTimeout,
	}}, workflow.Tier())
}

// TestWorkflowBase_TxReadAndWrapBuildTheRequestBoundary tests the workflow builders.
//
// Why this test is important:
//   - The request workflow owns the transaction: a write wired outside its transaction
//     leaves half a change behind on failure, and a read wired inside one holds a
//     transaction it does not need.
//
// What it tests:
//   - Tx, and RunTx (a saga's local step), each open exactly one transaction and run
//     the pipeline on its context.
//   - Read and Wrap open none (the strict transaction mock fails on a third call).
func TestWorkflowBase_TxReadAndWrapBuildTheRequestBoundary(t *testing.T) {
	t.Parallel()
	tx := mocks.NewMockTransactionManager(gomock.NewController(t))
	tx.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(ctx context.Context, fn func(context.Context) error) error {
			return fn(interfaces.WithInTx(ctx))
		})
	flows := fixtures.Workflows(tx)
	inTx := pipeline.Stage[int, bool](func(ctx context.Context, _ int) (bool, error) {
		return interfaces.InTx(ctx), nil
	})

	got, err := flows.Tx("write", inTx).Execute(t.Context(), 1)
	require.NoError(t, err)
	assert.True(t, got, "Tx runs the pipeline on the transaction's context")

	got, err = flows.RunTx(t.Context(), inTx, 1)
	require.NoError(t, err)
	assert.True(t, got, "RunTx runs the pipeline on the transaction's context")

	got, err = flows.Read("read", inTx).Execute(t.Context(), 1)
	require.NoError(t, err)
	assert.False(t, got, "Read opens no transaction")

	got, err = flows.Wrap("custom", interfaces.Workflow[int, bool](inTx)).
		Execute(t.Context(), 1)
	require.NoError(t, err)
	assert.False(t, got, "Wrap opens no transaction")
}

// TestWorkflowBaseTx_ReturnsTheCodedErrorAndNoResult tests a failed transaction.
//
// Why this test is important:
//   - The transport maps the error code to a status; losing it would turn a conflict or
//     an outage into a generic internal error, and a result whose transaction did not
//     commit must not reach the caller.
//
// What it tests:
//   - A pipeline error coded CONFLICT, and a commit error coded UNAVAILABLE after a
//     successful pipeline, each come back from Tx and from RunTx with their code and a
//     zero output.
func TestWorkflowBaseTx_ReturnsTheCodedErrorAndNoResult(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		pipeErr, commitErr error
		code               apperr.ErrorCode
	}{
		"pipeline error": {
			pipeErr: apperr.New(apperr.CodeConflict, "duplicate"), code: apperr.CodeConflict,
		},
		"commit error": {
			commitErr: apperr.New(apperr.CodeUnavailable, "commit failed"),
			code:      apperr.CodeUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tx := mocks.NewMockTransactionManager(gomock.NewController(t))
			tx.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
				func(ctx context.Context, fn func(context.Context) error) error {
					if err := fn(ctx); err != nil {
						return err
					}
					return tc.commitErr
				})
			pipe := pipeline.Stage[string, int](
				func(context.Context, string) (int, error) {
					return 7, tc.pipeErr
				},
			)

			flows := fixtures.Workflows(tx)
			for run, exec := range map[string]func() (int, error){
				"Tx": func() (int, error) {
					return flows.Tx("order", pipe).Execute(t.Context(), "x")
				},
				"RunTx": func() (int, error) { return flows.RunTx(t.Context(), pipe, "x") },
			} {
				out, err := exec()
				require.Error(t, err, run)
				assert.Equal(t, tc.code, apperr.Code(err), run)
				assert.Zero(t, out, run)
			}
		})
	}
}

// TestWorkflowBaseFunc_AppliesTimeoutAndRecovery tests a workflow's decoration.
//
// Why this test is important:
//   - A dropped timeout lets a hung dependency hold a request forever, and a dropped
//     recovery lets one panic crash the serving process.
//
// What it tests:
//   - With Deps.Timeout a workflow that waits on its context ends with the deadline.
//   - A panicking workflow returns an INTERNAL error; a nil fn panics at wiring time.
func TestWorkflowBaseFunc_AppliesTimeoutAndRecovery(t *testing.T) {
	t.Parallel()
	bounded := workflow.NewBase(nil, decorate.Deps{
		Observability: decorate.Observability{Logger: fixtures.NopLogger()},
		Timeout:       5 * time.Millisecond,
	})
	_, err := bounded.Func("slow", func(ctx context.Context, _ int) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}).Execute(t.Context(), 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = fixtures.Workflows(nil).
		Func("boom", func(context.Context, int) (int, error) {
			panic("boom")
		}).
		Execute(t.Context(), 1)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	assert.Panics(t, func() { fixtures.Workflows(nil).Func[int, int]("nil", nil) })
}
