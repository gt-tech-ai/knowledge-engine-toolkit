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
	fcache "github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// te and tp are the entity and list params the repository tests store.
type (
	te = fixtures.TestEntity
	tp = fixtures.TestParams
)

// cacheVersion is the entry version the caching tests encode with.
const cacheVersion = 3

// TestCacheKey tests the exported cache-key format shared by the caching decorator
// and out-of-band eviction sites.
//
// Why this test is important:
//   - CacheKey is the single source of truth for the key layout; a repository that
//     evicts a key computed differently from how the decorator wrote it would leak
//     stale data. Both sides must agree on the exact string.
//
// What it tests:
//   - CacheKey namespaces the id under "repo:<name>:<id>".
func TestCacheKey(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "repo:widgets:42", repository.CacheKey("widgets", 42))
}

// cachingRepo builds the read-through cache over store, optionally wiring the
// id-extractor that enables warm-on-create.
func cachingRepo(
	store interfaces.Store[te, tp, string],
	cache interfaces.ByteCache,
	withKeyFunc bool,
) interfaces.Store[te, tp, string] {
	c := repository.Cache[te, string]{
		Store:   cache,
		TTL:     time.Minute,
		Version: cacheVersion,
	}
	if withKeyFunc {
		c.IDOf = func(e *te) string { return e.ID }
	}
	return fixtures.Must(
		repository.NewEntity[interfaces.Repository[te, tp, string], te, tp, string](
			fixtures.Decorations(), "repo", store, &c,
		),
	)
}

// TestCached_Get tests the cache-aside read paths: a hit serves from
// cache without touching the backend, and every kind of miss (absent, stale
// version, corrupt bytes) falls through and back-fills the cache.
//
// Why this test is important:
//   - Cache-aside correctness is where stale reads and cache stampedes live: a hit
//     that returned the wrong entity, a stale-version entry served as fresh, or a
//     corrupt entry that errored instead of falling through would each be a
//     production data bug. On a miss the backend value must be fetched and cached.
//
// What it tests:
//   - Hit: cache returns a version-matching envelope; the backend is never called.
//   - Miss (absent): backend fetched, result cached, and the returned pointer is an
//     independent copy of the backend's.
//   - Miss (wrong version) and miss (corrupt bytes): both fall through to the
//     backend.
func TestCached_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stored := te{ID: "id", Name: "cached"}

	t.Run("hit serves from cache", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](
			ctrl,
		) // no Get expectation → must not be called
		enc, err := fcache.Encode(stored, cacheVersion)
		require.NoError(t, err)
		cache.EXPECT().Get(gomock.Any(), "repo:repo:id").Return(enc, true)

		got, err := cachingRepo(store, cache, true).Get(ctx, "id")
		require.NoError(t, err)
		assert.Equal(t, "cached", got.Name)
	})

	t.Run("miss fetches, caches, and copies", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		backend := &te{ID: "id", Name: "fresh"}
		cache.EXPECT().Get(gomock.Any(), "repo:repo:id").Return(nil, false)
		store.EXPECT().Get(gomock.Any(), "id").Return(backend, nil)
		cache.EXPECT().Set(gomock.Any(), "repo:repo:id", gomock.Any(), time.Minute)

		got, err := cachingRepo(store, cache, true).Get(ctx, "id")
		require.NoError(t, err)
		assert.Equal(t, "fresh", got.Name)
		assert.NotSame(t, backend, got, "the caller gets an independent copy")
	})

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "stale version", data: mustEncode(t, stored, cacheVersion+1)},
		{name: "corrupt bytes", data: []byte("not json")},
	} {
		t.Run("miss on "+tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			cache := mocks.NewMockByteCache(ctrl)
			store := mocks.NewMockStore[te, tp, string](ctrl)
			cache.EXPECT().Get(gomock.Any(), "repo:repo:id").Return(tc.data, true)
			store.EXPECT().Get(gomock.Any(), "id").Return(&te{ID: "id"}, nil)
			cache.EXPECT().Set(gomock.Any(), "repo:repo:id", gomock.Any(), gomock.Any())

			_, err := cachingRepo(store, cache, true).Get(ctx, "id")
			require.NoError(t, err)
		})
	}

	t.Run("backend error propagates", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		sentinel := apperr.New(apperr.CodeInternal, "backend down")
		cache.EXPECT().Get(gomock.Any(), "repo:repo:id").Return(nil, false)
		store.EXPECT().Get(gomock.Any(), "id").Return(nil, sentinel)

		_, err := cachingRepo(store, cache, true).Get(ctx, "id")
		assert.ErrorIs(t, err, sentinel)
	})
}

// mustEncode encodes value at version, failing the test on error.
func mustEncode(t *testing.T, value te, version int) []byte {
	t.Helper()
	b, err := fcache.Encode(value, version)
	require.NoError(t, err)
	return b
}

// TestCached_Writes tests that Create warms the cache (only with an
// id-extractor), and that Update and Delete evict on success but not on failure.
//
// Why this test is important:
//   - The write paths are what keep the cache coherent with the backend. A Create
//     that failed to warm would waste the first Get; an Update/Delete that evicted
//     on a failed write would thrash, and one that failed to evict on a successful
//     write would serve a stale or deleted record — a correctness bug.
//
// What it tests:
//   - Create with an id-extractor warms the cache; without one it does not.
//   - Create surfaces a backend error without caching.
//   - Update and Delete evict the key on success and skip eviction on failure.
func TestCached_Writes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("create warms with key func", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		store.EXPECT().
			Create(gomock.Any(), gomock.Any()).
			Return(&te{ID: "id", Name: "new"}, nil)
		cache.EXPECT().Set(gomock.Any(), "repo:repo:id", gomock.Any(), time.Minute)

		got, err := cachingRepo(store, cache, true).Create(ctx, &te{ID: "id"})
		require.NoError(t, err)
		assert.Equal(t, "new", got.Name)
	})

	t.Run("create without key func does not warm", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl) // no Set expectation → must not warm
		store := mocks.NewMockStore[te, tp, string](ctrl)
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&te{ID: "id"}, nil)

		_, err := cachingRepo(store, cache, false).Create(ctx, &te{ID: "id"})
		require.NoError(t, err)
	})

	t.Run("create error is not cached", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl) // no Set expectation
		store := mocks.NewMockStore[te, tp, string](ctrl)
		sentinel := apperr.New(apperr.CodeConflict, "dup")
		store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, sentinel)

		_, err := cachingRepo(store, cache, true).Create(ctx, &te{ID: "id"})
		assert.ErrorIs(t, err, sentinel)
	})

	t.Run("update evicts on success, not on failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		sentinel := apperr.New(apperr.CodeInternal, "boom")
		gomock.InOrder(
			store.EXPECT().
				Update(gomock.Any(), "id", gomock.Any()).
				Return(&te{ID: "id"}, nil),
			store.EXPECT().Update(gomock.Any(), "id", gomock.Any()).Return(nil, sentinel),
		)
		cache.EXPECT().Delete(gomock.Any(), "repo:repo:id") // exactly once (success only)

		repo := cachingRepo(store, cache, true)
		_, err := repo.Update(ctx, "id", &te{ID: "id"})
		require.NoError(t, err)
		_, err = repo.Update(ctx, "id", &te{ID: "id"})
		assert.ErrorIs(t, err, sentinel)
	})

	t.Run("delete evicts on success, not on failure", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		sentinel := apperr.New(apperr.CodeInternal, "boom")
		gomock.InOrder(
			store.EXPECT().Delete(gomock.Any(), "id").Return(nil),
			store.EXPECT().Delete(gomock.Any(), "id").Return(sentinel),
		)
		cache.EXPECT().Delete(gomock.Any(), "repo:repo:id") // exactly once (success only)

		repo := cachingRepo(store, cache, true)
		require.NoError(t, repo.Delete(ctx, "id"))
		assert.ErrorIs(t, repo.Delete(ctx, "id"), sentinel)
	})
}

// TestCached_ExistsAndList tests the remaining caching paths: Exists
// answers true from a cache hit without the backend, a miss falls through, and
// List passes straight through uncached.
//
// Why this test is important:
//   - Exists short-circuits on a cached entity (a cached entity definitively
//     exists), which must not regress into an unconditional backend call; List
//     must never be silently cached, since their results depend on inputs the
//     key does not capture.
//
// What it tests:
//   - Exists returns true from a cache hit without calling the backend, and calls
//     the backend on a miss.
//   - List delegates to the inner repository.
func TestCached_ExistsAndList(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("exists hit skips backend", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl) // no Exists expectation
		cache.EXPECT().
			Get(gomock.Any(), "repo:repo:id").
			Return(mustEncode(t, te{ID: "id"}, cacheVersion), true)

		ok, err := cachingRepo(store, cache, true).Exists(ctx, "id")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("exists miss hits backend", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl)
		store := mocks.NewMockStore[te, tp, string](ctrl)
		cache.EXPECT().Get(gomock.Any(), "repo:repo:id").Return(nil, false)
		store.EXPECT().Exists(gomock.Any(), "id").Return(true, nil)

		ok, err := cachingRepo(store, cache, true).Exists(ctx, "id")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("list passes through", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		cache := mocks.NewMockByteCache(ctrl) // never touched
		store := mocks.NewMockStore[te, tp, string](ctrl)
		store.EXPECT().
			List(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(&types.Page[te]{Total: 2}, nil)

		repo := cachingRepo(store, cache, true)
		page, err := repo.List(ctx, tp{}, types.PageRequest{})
		require.NoError(t, err)
		assert.Equal(t, int64(2), page.Total)
	})
}
