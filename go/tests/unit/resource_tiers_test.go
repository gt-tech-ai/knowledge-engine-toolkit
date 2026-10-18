package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/pipelines/pipeline"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/service"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/workflows/workflow"
)

// resourceUpdate is the update input of the test resource, a command rather than the
// entity itself.
type resourceUpdate struct {
	// Name is the entity's new name.
	Name string
}

// mockResource is a generated mock of the test resource's interfaces.Resource.
type mockResource = mocks.MockResource[te, tp, string, resourceCreate, resourceUpdate]

// expectEveryRole scripts one successful call of each of the resource's six roles on m.
func expectEveryRole(m *mockResource, w *te) {
	m.EXPECT().Get(gomock.Any(), "w1").Return(w, nil)
	m.EXPECT().
		List(gomock.Any(), tp{}, gomock.Any()).
		Return(&types.Page[te]{Total: 1}, nil)
	m.EXPECT().Create(gomock.Any(), resourceCreate{Name: "one"}).Return(w, nil)
	m.EXPECT().Update(gomock.Any(), "w1", resourceUpdate{Name: "two"}).Return(w, nil)
	m.EXPECT().Delete(gomock.Any(), "w1").Return(nil)
	m.EXPECT().Exists(gomock.Any(), "w1").Return(true, nil)
}

// resourceRoles is the interfaces.Resource method set every tier's Resource Base has.
type resourceRoles interface {
	Get(ctx context.Context, id string) (*te, error)
	List(ctx context.Context, params tp, page types.PageRequest) (*types.Page[te], error)
	Create(ctx context.Context, in resourceCreate) (*te, error)
	Update(ctx context.Context, id string, in resourceUpdate) (*te, error)
	Delete(ctx context.Context, id string) error
	Exists(ctx context.Context, id string) (bool, error)
}

// callEveryRole calls each of r's six roles once, requiring each to succeed.
func callEveryRole(t *testing.T, r resourceRoles) {
	t.Helper()
	_, err := r.Get(t.Context(), "w1")
	require.NoError(t, err)
	page, err := r.List(t.Context(), tp{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	_, err = r.Create(t.Context(), resourceCreate{Name: "one"})
	require.NoError(t, err)
	_, err = r.Update(t.Context(), "w1", resourceUpdate{Name: "two"})
	require.NoError(t, err)
	require.NoError(t, r.Delete(t.Context(), "w1"))
	ok, err := r.Exists(t.Context(), "w1")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestRepositoryBaseResource_DelegatesCommandInputsAndNeverRetriesACreate tests the
// repository tier's Resource base.
//
// Why this test is important:
//   - A resource's repository is assembled from the base over its store; a dropped call
//     corrupts every tier above it, and a retried create whose first attempt landed
//     after commit inserts a duplicate.
//
// What it tests:
//   - All six roles reach the store with their command-shaped inputs and return its
//     results.
//   - Every operation but Create goes through the retrier (five calls, not six).
func TestRepositoryBaseResource_DelegatesCommandInputsAndNeverRetriesACreate(
	t *testing.T,
) {
	t.Parallel()
	inner := mocks.NewMockResource[te, tp, string, resourceCreate, resourceUpdate](
		gomock.NewController(t),
	)
	expectEveryRole(inner, &te{ID: "w1"})
	repo := repository.NewBase("widgets", decorate.Deps{
		Retrier: passThroughRetrier(t, 5),
	}).Resource(inner)
	callEveryRole(t, repo)
}

// TestServiceBaseResource_ValidatesInputsBeforeTheRepository tests the service tier's
// Resource base.
//
// Why this test is important:
//   - A service is where a write's input is checked; an invalid create or update that
//     reached the repository would persist bad data.
//
// What it tests:
//   - All six roles reach the repository and return its results.
//   - A Create or Update whose input fails its validator returns the validator's error
//     and never reaches the repository (the strict mock allows no further call).
func TestServiceBaseResource_ValidatesInputsBeforeTheRepository(t *testing.T) {
	t.Parallel()
	inner := mocks.NewMockResource[te, tp, string, resourceCreate, resourceUpdate](
		gomock.NewController(t),
	)
	expectEveryRole(inner, &te{ID: "w1"})
	rejectEmpty := func(name string) error {
		if name == "" {
			return apperr.InvalidInput("name is required")
		}
		return nil
	}
	svc := service.NewBase("widgets", decorate.Deps{}).Resource(inner, service.Validators[
		resourceCreate, resourceUpdate,
	]{
		Create: func(in resourceCreate) error { return rejectEmpty(in.Name) },
		Update: func(in resourceUpdate) error { return rejectEmpty(in.Name) },
	})
	callEveryRole(t, svc)

	_, err := svc.Create(t.Context(), resourceCreate{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	_, err = svc.Update(t.Context(), "w1", resourceUpdate{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestPipelineBaseResource_StagesEveryRole tests the pipeline tier's Resource base.
//
// Why this test is important:
//   - A resource's pipeline is assembled from the base over its service; a role the
//     base dropped or misrouted would break every workflow above it.
//
// What it tests:
//   - All six roles reach the service with their inputs and return its results.
//   - Unit adapts an error-only step into a stage function whose result is empty.
func TestPipelineBaseResource_StagesEveryRole(t *testing.T) {
	t.Parallel()
	inner := mocks.NewMockResource[te, tp, string, resourceCreate, resourceUpdate](
		gomock.NewController(t),
	)
	expectEveryRole(inner, &te{ID: "w1"})
	callEveryRole(t, pipeline.NewBase(decorate.Deps{}).Resource("widgets", inner))

	called := ""
	stage := pipeline.NewBase(decorate.Deps{}).Stage("touch", pipeline.Unit(
		func(_ context.Context, id string) error {
			called = id
			return nil
		},
	))
	_, err := stage.Execute(t.Context(), "w1")
	require.NoError(t, err)
	assert.Equal(t, "w1", called)
}

// TestWorkflowBaseResource_ReadsWithoutATransactionAndCommitsEachWrite tests the
// workflow tier's Resource base.
//
// Why this test is important:
//   - The workflow tier is the only one that opens a transaction: a resource workflow
//     whose write ran outside one would be refused by every store, and a read that
//     opened one would hold a connection for nothing.
//
// What it tests:
//   - Get, List and Exists run with no transaction open.
//   - Create, Update and Delete each run in exactly one transaction (three in all).
func TestWorkflowBaseResource_ReadsWithoutATransactionAndCommitsEachWrite(t *testing.T) {
	t.Parallel()
	inner := mocks.NewMockResource[te, tp, string, resourceCreate, resourceUpdate](
		gomock.NewController(t),
	)
	w := &te{ID: "w1"}
	readOutsideTx := func(ctx context.Context) {
		assert.False(t, interfaces.InTx(ctx), "a read opens no transaction")
	}
	writeInTx := func(ctx context.Context) {
		assert.True(t, interfaces.InTx(ctx), "a write runs in a transaction")
	}
	inner.EXPECT().Get(gomock.Any(), "w1").DoAndReturn(
		func(ctx context.Context, _ string) (*te, error) {
			readOutsideTx(ctx)
			return w, nil
		})
	inner.EXPECT().List(gomock.Any(), tp{}, gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ tp, _ types.PageRequest) (*types.Page[te], error) {
			readOutsideTx(ctx)
			return &types.Page[te]{Total: 1}, nil
		})
	inner.EXPECT().Exists(gomock.Any(), "w1").DoAndReturn(
		func(ctx context.Context, _ string) (bool, error) {
			readOutsideTx(ctx)
			return true, nil
		})
	inner.EXPECT().Create(gomock.Any(), resourceCreate{Name: "one"}).DoAndReturn(
		func(ctx context.Context, _ resourceCreate) (*te, error) {
			writeInTx(ctx)
			return w, nil
		})
	inner.EXPECT().Update(gomock.Any(), "w1", resourceUpdate{Name: "two"}).DoAndReturn(
		func(ctx context.Context, _ string, _ resourceUpdate) (*te, error) {
			writeInTx(ctx)
			return w, nil
		})
	inner.EXPECT().Delete(gomock.Any(), "w1").DoAndReturn(
		func(ctx context.Context, _ string) error {
			writeInTx(ctx)
			return nil
		})

	flows := workflow.NewBase("widgets", txManager(t, 3, nil), decorate.Deps{})
	callEveryRole(t, workflow.NewResource(flows, inner))
}
