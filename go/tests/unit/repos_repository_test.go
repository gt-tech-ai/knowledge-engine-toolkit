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
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
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

// widgetsQueries is the contract of the test repository's custom queries.
type widgetsQueries interface {
	// CountByOwner counts an owner's widgets.
	CountByOwner(ctx context.Context) (int, error)

	// Slow waits for its deadline.
	Slow(ctx context.Context) (int, error)
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
	base := fixtures.Must(repository.NewBase[widgetsQueries](decorate.Fixed(decorate.Deps{
		Timeout: 20 * time.Millisecond,
		Retrier: passThroughRetrier(t, 2),
	}), "widgets"))
	assert.Equal(t, "widgets", base.Name())

	got, err := base.Exec(
		t.Context(),
		"count_by_owner",
		func(context.Context) (int, error) {
			return 4, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 4, got)

	_, err = base.Exec(t.Context(), "slow", func(ctx context.Context) (int, error) {
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

// TestRepositoryEntity_NarrowContracts tests repositories whose contracts have only some
// roles.
//
// Why this test is important:
//   - A read model, a keyed resource and an append-only table each expose only the
//     roles they honor; an append inside the caller's transaction must not be retried or
//     timed out, which would corrupt that transaction.
//
// What it tests:
//   - A read-only and a list-less repository build over stores with only their roles and
//     delegate to them.
//   - An insert-only repository's Insert reaches the store without the retrier (the
//     strict mock allows no call) or a deadline, and returns the store's error.
func TestRepositoryEntity_NarrowContracts(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	decs := decorate.Fixed(decorate.Deps{
		Retrier: passThroughRetrier(t, 2),
		Timeout: time.Minute,
	})

	reads := mocks.NewMockReadListExistStore[te, tp, string](ctrl)
	reads.EXPECT().Exists(gomock.Any(), "w1").Return(true, nil)
	ro := fixtures.Must(repository.NewEntity[
		interfaces.ReadListExistStore[te, tp, string], te, tp, string,
	](decs, "widgets", reads, nil))
	ok, err := ro.Exists(t.Context(), "w1")
	require.NoError(t, err)
	assert.True(t, ok)

	keyed := mocks.NewMockCRUDNoListStore[te, string](ctrl)
	keyed.EXPECT().Delete(gomock.Any(), "w1").Return(nil)
	kr := fixtures.Must(repository.NewEntity[
		interfaces.CRUDNoListStore[te, string], te, tp, string,
	](decs, "widgets", keyed, nil))
	require.NoError(t, kr.Delete(t.Context(), "w1"))

	appends := mocks.NewMockInsertOnlyStore[te](ctrl)
	conflict := apperr.New(apperr.CodeConflict, "duplicate")
	appends.EXPECT().Insert(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ *te) error {
			_, bounded := ctx.Deadline()
			assert.False(t, bounded, "an in-transaction append is never timed out")
			return conflict
		})
	ar := fixtures.Must(repository.NewEntity[
		interfaces.InsertOnlyStore[te], te, tp, string,
	](decs, "widgets", appends, nil))
	require.ErrorIs(t, ar.Insert(t.Context(), &te{ID: "w1"}), conflict)
}
