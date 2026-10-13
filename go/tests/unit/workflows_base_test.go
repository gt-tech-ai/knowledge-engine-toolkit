package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	workflowscfg "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/workflows"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// baseTxKey marks the context the mocked transaction manager hands its fn.
type baseTxKey struct{}

// TestWorkflowBase_BuildsTheRequestBoundary tests the workflow base's builders.
//
// Why this test is important:
//   - The request workflow is the one decorated boundary per request and the owner of
//     the transaction: a write wired outside its transaction leaves half a change behind
//     on failure, and a read wired inside one holds a transaction it does not need.
//
// What it tests:
//   - Tx opens exactly one transaction and runs the pipeline on its context.
//   - Read opens none (the strict transaction mock fails the test if it is called).
//   - Wrap bounds a custom workflow by the configured timeout.
func TestWorkflowBase_BuildsTheRequestBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tx := mocks.NewMockTransactionManager(gomock.NewController(t))
	tx.EXPECT().WithTransaction(gomock.Any(), gomock.Any()).Times(1).DoAndReturn(
		func(ctx context.Context, fn func(context.Context) error) error {
			return fn(context.WithValue(ctx, baseTxKey{}, true))
		},
	)
	base := workflow.NewBase(tx, decorate.Observability{},
		workflowscfg.Config{Timeout: 20 * time.Millisecond})
	inTx := pipeline.NewBasePipeline(func(ctx context.Context, _ int) (bool, error) {
		marked, _ := ctx.Value(baseTxKey{}).(bool)
		return marked, nil
	})

	got, err := base.Tx("write", inTx).Execute(ctx, 1)
	require.NoError(t, err)
	assert.True(t, got, "Tx runs the pipeline on the transaction's context")

	got, err = base.Read("read", inTx).Execute(ctx, 1)
	require.NoError(t, err)
	assert.False(t, got, "Read opens no transaction")

	_, err = base.Wrap("slow", workflow.NewBaseWorkflow(
		func(ctx context.Context, _ int) (int, error) {
			<-ctx.Done()
			return 0, ctx.Err()
		},
	)).Execute(ctx, 1)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
