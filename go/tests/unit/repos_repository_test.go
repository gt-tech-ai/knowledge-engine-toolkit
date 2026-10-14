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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestRepositoryTier_Order tests the repository tier's decorator order.
//
// Why this test is important:
//   - The order is the tier's documented contract (ARCHITECTURE.md#decorator-order):
//     retry sits inside the breaker and the timeout, so each attempt is observed and a
//     retried call is still bounded and shed by an open breaker.
//
// What it tests:
//   - The tier is named "repository" and orders tracing → metrics → logging → timeout →
//     circuit breaker → retry.
func TestRepositoryTier_Order(t *testing.T) {
	t.Parallel()
	assert.Equal(t, decorate.Tier{Name: "repository", Order: []decorate.Layer{
		decorate.LayerTracing, decorate.LayerMetrics, decorate.LayerLogging,
		decorate.LayerTimeout, decorate.LayerCircuitBreaker, decorate.LayerRetry,
	}}, repository.Tier())
}

// passThroughRetrier is a strict retrier mock that runs each operation once and counts
// how often it was asked to.
func passThroughRetrier(t *testing.T, times int) interfaces.Retrier {
	t.Helper()
	r := mocks.NewMockRetrier(gomock.NewController(t))
	r.EXPECT().Retry(gomock.Any(), gomock.Any()).Times(times).DoAndReturn(
		func(_ context.Context, op func() error) error { return op() })
	return r
}

// TestRepositoryBase_RunsCustomQueriesThroughTheChain tests the embeddable base.
//
// Why this test is important:
//   - A consumer repository's custom query must get the timeout and retry the decorated
//     CRUD gets; a query outside the chain hangs on a stuck database and is never
//     retried on a transient failure.
//
// What it tests:
//   - Exec returns the query's result through the retrier; Name is the repository's.
//   - A query over the timeout ends with the deadline; a zero Base runs undecorated.
func TestRepositoryBase_RunsCustomQueriesThroughTheChain(t *testing.T) {
	t.Parallel()
	base := repository.NewBase("widgets", decorate.Deps{
		Timeout: 20 * time.Millisecond,
		Retrier: passThroughRetrier(t, 2),
	})
	assert.Equal(t, "widgets", base.Name())

	got, err := base.Exec(
		t.Context(),
		"CountByOwner",
		func(context.Context) (int, error) {
			return 4, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 4, got)

	_, err = base.Exec(t.Context(), "Slow", func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	got, err = repository.Base{}.Exec(
		t.Context(),
		"Plain",
		func(context.Context) (int, error) {
			return 1, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

// TestRepositoryBaseCRUD_DelegatesAndNeverRetriesACreate tests the decorated CRUD
// repository.
//
// Why this test is important:
//   - Every entity read and write of a resource runs through it; a dropped call
//     corrupts every service above it, and a retried create whose first attempt landed
//     after commit inserts a duplicate.
//
// What it tests:
//   - Get, List, Create, Update, Delete and Exists reach the store and return its
//     results.
//   - Every operation but Create goes through the retrier (five calls, not six).
func TestRepositoryBaseCRUD_DelegatesAndNeverRetriesACreate(t *testing.T) {
	t.Parallel()
	store := mocks.NewMockStore[te, tp, string](gomock.NewController(t))
	repo := repository.NewBase("widgets", decorate.Deps{
		Retrier: passThroughRetrier(t, 5),
	}).CRUD(store)
	w := &te{ID: "w1", Name: "one"}
	store.EXPECT().Get(gomock.Any(), "w1").Return(w, nil)
	store.EXPECT().
		List(gomock.Any(), tp{}, gomock.Any()).
		Return(&types.Page[te]{Total: 1}, nil)
	store.EXPECT().Create(gomock.Any(), w).Return(w, nil)
	store.EXPECT().Update(gomock.Any(), "w1", w).Return(w, nil)
	store.EXPECT().Delete(gomock.Any(), "w1").Return(nil)
	store.EXPECT().Exists(gomock.Any(), "w1").Return(true, nil)

	got, err := repo.Get(t.Context(), "w1")
	require.NoError(t, err)
	assert.Same(t, w, got)
	page, err := repo.List(t.Context(), tp{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)
	_, err = repo.Create(t.Context(), w)
	require.NoError(t, err)
	_, err = repo.Update(t.Context(), "w1", w)
	require.NoError(t, err)
	require.NoError(t, repo.Delete(t.Context(), "w1"))
	ok, err := repo.Exists(t.Context(), "w1")
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestRepositoryBase_RoleScopedShapes tests the narrowed repositories.
//
// Why this test is important:
//   - A read model, a keyed resource and an append-only table each expose only the
//     roles they honor; an append inside the caller's transaction must not be retried or
//     timed out, which would corrupt that transaction.
//
// What it tests:
//   - ReadListExist and CRUDNoList delegate their operations to the store.
//   - TxSafeInsert's Insert reaches the store without the retrier (the strict mock
//     allows no call) and returns the store's error.
func TestRepositoryBase_RoleScopedShapes(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	base := repository.NewBase("widgets", decorate.Deps{
		Retrier: passThroughRetrier(t, 2),
		Timeout: time.Minute,
	})

	reads := mocks.NewMockReadListExistStore[te, tp, string](ctrl)
	reads.EXPECT().Exists(gomock.Any(), "w1").Return(true, nil)
	ok, err := base.ReadListExist(reads).Exists(t.Context(), "w1")
	require.NoError(t, err)
	assert.True(t, ok)

	keyed := mocks.NewMockCRUDNoListStore[te, string](ctrl)
	keyed.EXPECT().Delete(gomock.Any(), "w1").Return(nil)
	require.NoError(t, base.CRUDNoList(keyed).Delete(t.Context(), "w1"))

	appends := mocks.NewMockInsertOnlyStore[te](ctrl)
	conflict := apperr.New(apperr.CodeConflict, "duplicate")
	appends.EXPECT().Insert(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ *te) error {
			_, bounded := ctx.Deadline()
			assert.False(t, bounded, "an in-transaction append is never timed out")
			return conflict
		})
	require.ErrorIs(
		t,
		base.TxSafeInsert(appends).Insert(t.Context(), &te{ID: "w1"}),
		conflict,
	)
}
