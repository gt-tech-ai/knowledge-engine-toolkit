package unit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestBaseRepository_DelegatesEveryOperationToStore tests that BaseRepository is a
// faithful pass-through to its Store for the full CRUD surface, and that Name
// reports the configured label.
//
// Why this test is important:
//   - BaseRepository is the innermost layer every decorated repository wraps; if it
//     dropped a call or mangled a result, every service's data access would be
//     wrong regardless of the decorators layered on top. Name is the label the
//     metrics/logging/tracing decorators stamp on their output.
//
// What it tests:
//   - Create → Get returns the stored entity, List returns the store's page,
//     Exists reflects presence, Delete removes the entity, Update replaces it, and
//     Name returns the value passed to New — all observed end-to-end through the
//     in-memory StubStore.
func TestBaseRepository_DelegatesEveryOperationToStore(t *testing.T) {
	t.Parallel()

	store := fixtures.StubStore()
	repo := repository.New[fixtures.TestEntity, fixtures.TestParams, string](
		store,
		"widgets",
	)
	ctx := context.Background()

	assert.Equal(t, "widgets", repo.Name())

	created, err := repo.Create(ctx, &fixtures.TestEntity{ID: "w1", Name: "one"})
	require.NoError(t, err)
	assert.Equal(t, "w1", created.ID)

	got, err := repo.Get(ctx, "w1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "one", got.Name)

	exists, err := repo.Exists(ctx, "w1")
	require.NoError(t, err)
	assert.True(t, exists)

	updated, err := repo.Update(ctx, "w1", &fixtures.TestEntity{ID: "w1", Name: "two"})
	require.NoError(t, err)
	assert.Equal(t, "two", updated.Name)

	page, err := repo.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), page.Total)

	require.NoError(t, repo.Delete(ctx, "w1"))
	exists, err = repo.Exists(ctx, "w1")
	require.NoError(t, err)
	assert.False(t, exists)
}

// TestBaseRepository_PropagatesStoreErrors tests that a store failure surfaces
// unchanged through every BaseRepository method rather than being swallowed.
//
// Why this test is important:
//   - The repository layer is intentionally thin; a store error (a dropped
//     connection, a constraint violation) must reach the caller so the resilience
//     decorators and the service layer can react. A method that hid the error would
//     make a broken write look successful.
//
// What it tests:
//   - With a store whose every method returns a sentinel error, Get/List/Create/
//     Update/Delete/Exists all return that exact error.
func TestBaseRepository_PropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[fixtures.TestEntity, fixtures.TestParams, string](ctrl)
	sentinel := apperr.New(apperr.CodeInternal, "store down")

	store.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, sentinel)
	store.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, sentinel)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, sentinel)
	store.EXPECT().Update(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, sentinel)
	store.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(sentinel)
	store.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, sentinel)

	repo := repository.New[fixtures.TestEntity, fixtures.TestParams, string](
		store,
		"widgets",
	)
	ctx := context.Background()

	_, err := repo.Get(ctx, "x")
	require.ErrorIs(t, err, sentinel)
	_, err = repo.List(ctx, fixtures.TestParams{}, types.PageRequest{})
	require.ErrorIs(t, err, sentinel)
	_, err = repo.Create(ctx, &fixtures.TestEntity{})
	require.ErrorIs(t, err, sentinel)
	_, err = repo.Update(ctx, "x", &fixtures.TestEntity{})
	require.ErrorIs(t, err, sentinel)
	require.ErrorIs(t, repo.Delete(ctx, "x"), sentinel)
	_, err = repo.Exists(ctx, "x")
	assert.ErrorIs(t, err, sentinel)
}
