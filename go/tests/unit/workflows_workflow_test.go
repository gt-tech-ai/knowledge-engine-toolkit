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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/loop"
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

// txManager returns a strict transaction manager mock expecting n WithTransaction
// calls, each running fn on a context marked in-transaction and returning commitErr
// after a successful fn.
func txManager(t *testing.T, n int, commitErr error) *mocks.MockTransactionManager {
	t.Helper()
	tx := mocks.NewMockTransactionManager(gomock.NewController(t))
	tx.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Times(n).DoAndReturn(
		func(ctx context.Context, fn func(context.Context) error) error {
			if err := fn(interfaces.WithInTx(ctx)); err != nil {
				return err
			}
			return commitErr
		})
	return tx
}

// testFlows is the contract of the test workflows: one operation per shape tested.
type testFlows interface {
	Read(ctx context.Context) error
	Command(ctx context.Context) error
	Saga(ctx context.Context) error
	Order(ctx context.Context) error
	Nested(ctx context.Context) error
	Unmanaged(ctx context.Context) error
	Slow(ctx context.Context) error
	Boom(ctx context.Context) error
}

// testWorkflow is an undecorated workflow.Base over testFlows whose transactions open
// through tx.
func testWorkflow(tx interfaces.TransactionManager) workflow.Base {
	return fixtures.Must(workflow.NewBase[testFlows](fixtures.Decorations(), "test", tx))
}

// inTx reports whether ctx is in a transaction, as a workflow body.
func inTx(ctx context.Context) (bool, error) { return interfaces.InTx(ctx), nil }

// TestWorkflowBase_ReadCommandAndSagaShapes tests the three workflow shapes.
//
// Why this test is important:
//   - The workflow owns the transaction: a write run outside its transaction leaves half
//     a change behind on failure, and a read run inside one holds a transaction it does
//     not need.
//
// What it tests:
//   - Exec (a read) opens no transaction; ExecTx (a command) and InTx (a saga step) each
//     open exactly one and run fn on its context (the strict mock fails on a third).
//   - Name is the workflow's.
func TestWorkflowBase_ReadCommandAndSagaShapes(t *testing.T) {
	t.Parallel()
	flows := testWorkflow(txManager(t, 2, nil))
	assert.Equal(t, "test", flows.Name())

	got, err := flows.Exec(t.Context(), "read", inTx)
	require.NoError(t, err)
	assert.False(t, got, "Exec opens no transaction")

	got, err = flows.ExecTx(t.Context(), "command", inTx)
	require.NoError(t, err)
	assert.True(t, got, "ExecTx runs fn on the transaction's context")

	got, err = flows.Exec(t.Context(), "saga", func(ctx context.Context) (bool, error) {
		return flows.InTx(ctx, inTx)
	})
	require.NoError(t, err)
	assert.True(t, got, "InTx runs a saga step on its transaction's context")
}

// TestWorkflowBase_TransactionErrorsKeepTheirCodeAndNoResult tests a failed transaction.
//
// Why this test is important:
//   - The transport maps the error code to a status; losing it would turn a conflict or
//     an outage into a generic internal error, and a result whose transaction did not
//     commit must not reach the caller.
//
// What it tests:
//   - A body error coded CONFLICT, and a commit error coded UNAVAILABLE after a
//     successful body, each come back from ExecTx and from InTx with their code and a
//     zero output.
func TestWorkflowBase_TransactionErrorsKeepTheirCodeAndNoResult(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		bodyErr, commitErr error
		code               apperr.ErrorCode
	}{
		"body error": {
			bodyErr: apperr.New(apperr.CodeConflict, "duplicate"), code: apperr.CodeConflict,
		},
		"commit error": {
			commitErr: apperr.New(apperr.CodeUnavailable, "commit failed"),
			code:      apperr.CodeUnavailable,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			flows := testWorkflow(txManager(t, 2, tc.commitErr))
			body := func(context.Context) (int, error) { return 7, tc.bodyErr }
			for shape, run := range map[string]func() (int, error){
				"ExecTx": func() (int, error) { return flows.ExecTx(t.Context(), "order", body) },
				"InTx":   func() (int, error) { return flows.InTx(t.Context(), body) },
			} {
				out, err := run()
				require.Error(t, err, shape)
				assert.Equal(t, tc.code, apperr.Code(err), shape)
				assert.Zero(t, out, shape)
			}
		})
	}
}

// TestWorkflowBase_RefusesANestedOrUnmanagedTransaction tests the transaction guards.
//
// Why this test is important:
//   - A transaction opened inside another runs on a second connection and commits on its
//     own, so a failure of the outer one leaves the inner write behind; a base with no
//     transaction manager would otherwise panic on the first command.
//
// What it tests:
//   - ExecTx and InTx on a context already in a transaction fail INTERNAL without
//     calling the transaction manager or the body.
//   - ExecTx on a base with no transaction manager fails INTERNAL.
func TestWorkflowBase_RefusesANestedOrUnmanagedTransaction(t *testing.T) {
	t.Parallel()
	flows := testWorkflow(txManager(t, 0, nil))
	nested := interfaces.WithInTx(t.Context())
	body := func(context.Context) (int, error) {
		t.Error("the body of a refused transaction must not run")
		return 0, nil
	}

	_, err := flows.ExecTx(nested, "nested", body)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	_, err = flows.InTx(nested, body)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))

	_, err = testWorkflow(nil).ExecTx(t.Context(), "unmanaged", body)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestWorkflowBasePass_DecoratesALoopPass tests the loop-pass decorator.
//
// Why this test is important:
//   - A background loop's pass must be observed and recovered like a request; a pass
//     that panicked undecorated would stop its loop.
//
// What it tests:
//   - Pass, used as a loop.Decorator, runs the pass with no transaction and returns its
//     error; a panicking pass returns INTERNAL.
func TestWorkflowBasePass_DecoratesALoopPass(t *testing.T) {
	t.Parallel()
	flows := testWorkflow(txManager(t, 0, nil))
	var decorator loop.Decorator = flows.Pass
	var sawTx bool
	pass := decorator("relay-pass", loop.Pass(func(ctx context.Context) error {
		sawTx = interfaces.InTx(ctx)
		return apperr.New(apperr.CodeUnavailable, "sink down")
	}))
	_, err := pass.Execute(t.Context(), struct{}{})
	assert.Equal(t, apperr.CodeUnavailable, apperr.Code(err))
	assert.False(t, sawTx, "a pass runs with no transaction")

	_, err = decorator("boom-pass", loop.Pass(func(context.Context) error {
		panic("boom")
	})).Execute(t.Context(), struct{}{})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}

// TestWorkflowBase_AppliesTimeoutAndRecovery tests a workflow's decoration.
//
// Why this test is important:
//   - A dropped timeout lets a hung dependency hold a request forever, and a dropped
//     recovery lets one panic crash the serving process.
//
// What it tests:
//   - With Deps.Timeout an operation that waits on its context ends with the deadline.
//   - A panicking operation returns an INTERNAL error.
func TestWorkflowBase_AppliesTimeoutAndRecovery(t *testing.T) {
	t.Parallel()
	bounded := fixtures.Must(workflow.NewBase[testFlows](decorate.Fixed(decorate.Deps{
		Observability: decorate.Observability{Logger: fixtures.NopLogger()},
		Timeout:       5 * time.Millisecond,
	}), "bounded", nil))
	_, err := bounded.Exec(t.Context(), "slow", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	_, err = testWorkflow(nil).
		Exec(t.Context(), "boom", func(context.Context) (int, error) {
			panic("boom")
		})
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
}
