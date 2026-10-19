package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// widgetsService is the contract of the CRUD widgets at the pipeline and workflow tiers.
type widgetsService = interfaces.Service[te, tp, string]

// crudRoles is the interfaces.Service method set the CRUD bases return.
type crudRoles interface {
	Get(ctx context.Context, id string) (*te, error)
	List(ctx context.Context, params tp, page types.PageRequest) (*types.Page[te], error)
	Create(ctx context.Context, entity *te) (*te, error)
	Update(ctx context.Context, id string, entity *te) (*te, error)
	Delete(ctx context.Context, id string) error
}

// expectEveryCRUDRole scripts one call of each of the service's five roles on m; check
// sees each call's context and whether the role is a write.
func expectEveryCRUDRole(
	m *mocks.MockService[te, tp, string],
	w *te,
	check func(ctx context.Context, write bool),
) {
	m.EXPECT().Get(gomock.Any(), "w1").DoAndReturn(
		func(ctx context.Context, _ string) (*te, error) {
			check(ctx, false)
			return w, nil
		})
	m.EXPECT().List(gomock.Any(), tp{}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ tp, _ types.PageRequest) (*types.Page[te], error) {
			check(ctx, false)
			return &types.Page[te]{Total: 1}, nil
		})
	m.EXPECT().Create(gomock.Any(), w).DoAndReturn(
		func(ctx context.Context, e *te) (*te, error) {
			check(ctx, true)
			return e, nil
		})
	m.EXPECT().Update(gomock.Any(), "w1", w).DoAndReturn(
		func(ctx context.Context, _ string, e *te) (*te, error) {
			check(ctx, true)
			return e, nil
		})
	m.EXPECT().Delete(gomock.Any(), "w1").DoAndReturn(
		func(ctx context.Context, _ string) error {
			check(ctx, true)
			return nil
		})
}

// callEveryCRUDRole calls each of r's five roles once, requiring each to succeed and
// return the inner result.
func callEveryCRUDRole(t *testing.T, r crudRoles, w *te) {
	t.Helper()
	got, err := r.Get(t.Context(), "w1")
	require.NoError(t, err)
	assert.Same(t, w, got)
	page, err := r.List(t.Context(), tp{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	got, err = r.Create(t.Context(), w)
	require.NoError(t, err)
	assert.Same(t, w, got)
	got, err = r.Update(t.Context(), "w1", w)
	require.NoError(t, err)
	assert.Same(t, w, got)
	require.NoError(t, r.Delete(t.Context(), "w1"))
}

// TestPipelineBaseCRUD_StagesEveryRole tests the pipeline tier's CRUD base.
//
// Why this test is important:
//   - A CRUD resource's pipeline is assembled from the base over its service; a role the
//     base dropped or misrouted would break every workflow above it.
//
// What it tests:
//   - All five roles reach the service with their inputs and return its results.
func TestPipelineBaseCRUD_StagesEveryRole(t *testing.T) {
	t.Parallel()
	inner := mocks.NewMockService[te, tp, string](gomock.NewController(t))
	w := &te{ID: "w1"}
	expectEveryCRUDRole(inner, w, func(context.Context, bool) {})
	pipe := fixtures.Must(
		pipeline.NewBase[widgetsService](fixtures.Decorations(), "widgets"),
	)
	callEveryCRUDRole(t, pipe.CRUD(inner), w)
}

// TestWorkflowBaseCRUD_ReadsWithoutATransactionAndCommitsEachWrite tests the workflow
// tier's CRUD base.
//
// Why this test is important:
//   - The workflow tier is the only one that opens a transaction: a CRUD workflow whose
//     write ran outside one would be refused by every store, and a read that opened one
//     would hold a connection for nothing.
//
// What it tests:
//   - Get and List run with no transaction open.
//   - Create, Update and Delete each run in exactly one transaction (three in all; the
//     strict transaction mock fails on a fourth).
func TestWorkflowBaseCRUD_ReadsWithoutATransactionAndCommitsEachWrite(t *testing.T) {
	t.Parallel()
	inner := mocks.NewMockService[te, tp, string](gomock.NewController(t))
	w := &te{ID: "w1"}
	expectEveryCRUDRole(inner, w, func(ctx context.Context, write bool) {
		assert.Equal(t, write, interfaces.InTx(ctx), "only a write runs in a transaction")
	})
	flows := fixtures.Must(workflow.NewBase[widgetsService](
		decorate.Fixed(decorate.Deps{}), "widgets", txManager(t, 3, nil),
	))
	callEveryCRUDRole(t, flows.CRUD(inner), w)
}
