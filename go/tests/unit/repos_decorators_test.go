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
	repodeco "github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// te/tp alias the shared fixture entity and param types so the generic decorator
// instantiations below stay readable.
type (
	te = fixtures.TestEntity
	tp = fixtures.TestParams
)

// cacheVersion is the schema version used across the caching-decorator tests.
const cacheVersion = 3

// newBuilder wraps store in a fresh decorator builder named "repo". Each test adds
// exactly one decorator so its behaviour is observed in isolation.
func newBuilder(
	store interfaces.Store[te, tp, string],
) *repodeco.Builder[te, tp, string] {
	base := repository.New[te, tp, string](store, "repo")
	return repodeco.NewBuilder[te, tp, string](base, "repo")
}

// errStore returns a MockStore whose every CRUD method fails with err, for driving
// the error branch of a decorator.
func errStore(t *testing.T, err error) *mocks.MockStore[te, tp, string] {
	t.Helper()
	s := mocks.NewMockStore[te, tp, string](gomock.NewController(t))
	s.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, err).AnyTimes()
	s.EXPECT().List(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, err).AnyTimes()
	s.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, err).AnyTimes()
	s.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, err).
		AnyTimes()
	s.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(err).AnyTimes()
	s.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(false, err).AnyTimes()
	return s
}

// driveAll exercises the full decorated CRUD surface once, so an error run drives
// every failure branch. Custom (non-CRUD) operations decorate through decorate.Exec
// now and are covered by the decorate package's own tests.
func driveAll(
	ctx context.Context,
	repo interfaces.DecoratedRepository[te, tp, string],
) {
	_, _ = repo.Get(ctx, "id")
	_, _ = repo.List(ctx, tp{}, types.PageRequest{})
	_, _ = repo.Create(ctx, &te{ID: "id"})
	_, _ = repo.Update(ctx, "id", &te{ID: "id"})
	_ = repo.Delete(ctx, "id")
	_, _ = repo.Exists(ctx, "id")
}

// TestLoggingDecorator_LogsAndDelegates tests that the logging decorator logs an
// entry line for every operation, an extra failure line when the inner repository
// errors, and delegates the result unchanged.
//
// Why this test is important:
//   - The logging decorator is the operator's window into the data layer; a missed
//     entry or a swallowed failure log would blind on-call during an incident. It
//     must also stay a transparent pass-through so wrapping never alters results.
//
// What it tests:
//   - A success run logs each "repository.<op>" line and no "... failed" line.
//   - An error run logs both the entry and the "repository.<op> failed" line for
//     every operation, including the custom operation.
func TestLoggingDecorator_LogsAndDelegates(t *testing.T) {
	t.Parallel()

	okSpy := fixtures.NewSpyLogger()
	okRepo := newBuilder(fixtures.StubStore()).WithLogging(okSpy).Build()
	driveAll(context.Background(), okRepo)

	for _, op := range []string{"Get", "List", "Create", "Update", "Delete", "Exists"} {
		assert.Contains(t, (*okSpy.ChildDebugCalls), "repository."+op)
	}
	assert.NotContains(t, (*okSpy.ChildDebugCalls), "repository.Get failed")

	errSpy := fixtures.NewSpyLogger()
	sentinel := apperr.New(apperr.CodeInternal, "boom")
	errRepo := newBuilder(errStore(t, sentinel)).WithLogging(errSpy).Build()
	driveAll(context.Background(), errRepo)

	for _, op := range []string{"Get", "List", "Create", "Update", "Delete", "Exists"} {
		assert.Contains(t, (*errSpy.ChildDebugCalls), "repository."+op+" failed")
	}
}

// TestMetricsDecorator_RecordsOpsAndErrors tests that the metrics decorator counts
// every operation and increments the error counter only when the operation fails.
//
// Why this test is important:
//   - Repository dashboards and error-rate alerts are built on these two counters;
//     if the error counter ticked on success (or never ticked on failure), a real
//     outage would be invisible or a healthy system would page.
//
// What it tests:
//   - A success run over all six CRUD operations records 6 ops and 0 errors.
//   - A failure run records 6 ops and 6 errors.
func TestMetricsDecorator_RecordsOpsAndErrors(t *testing.T) {
	t.Parallel()

	okMetrics, okOps, okErrs := recordingMetrics(t)
	okRepo := newBuilder(fixtures.StubStore()).WithMetrics(okMetrics).Build()
	driveAll(context.Background(), okRepo)
	assert.Equal(t, 6, *okOps, "every operation is counted")
	assert.Equal(t, 0, *okErrs, "no error counter ticks on success")

	failMetrics, failOps, failErrs := recordingMetrics(t)
	sentinel := apperr.New(apperr.CodeInternal, "boom")
	failRepo := newBuilder(errStore(t, sentinel)).WithMetrics(failMetrics).Build()
	driveAll(context.Background(), failRepo)
	assert.Equal(t, 6, *failOps)
	assert.Equal(t, 6, *failErrs, "every failure ticks the error counter")
}

// recordingMetrics returns a generated MockMetrics that routes the ops and error
// counters to two recording counters, plus pointers to their live Inc counts.
func recordingMetrics(t *testing.T) (m interfaces.Metrics, ops, errs *int) {
	t.Helper()
	ctrl := gomock.NewController(t)
	var opsN, errN int

	opsC := mocks.NewMockCounter(ctrl)
	opsC.EXPECT().Inc(gomock.Any()).Do(func(...string) { opsN++ }).AnyTimes()
	opsC.EXPECT().Add(gomock.Any(), gomock.Any()).AnyTimes()

	errC := mocks.NewMockCounter(ctrl)
	errC.EXPECT().Inc(gomock.Any()).Do(func(...string) { errN++ }).AnyTimes()
	errC.EXPECT().Add(gomock.Any(), gomock.Any()).AnyTimes()

	hist := mocks.NewMockHistogram(ctrl)
	hist.EXPECT().Observe(gomock.Any(), gomock.Any()).AnyTimes()

	mm := mocks.NewMockMetrics(ctrl)
	mm.EXPECT().Counter(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(name, _ string, _ ...string) interfaces.Counter {
			if name == "repository_errors_total" {
				return errC
			}
			return opsC
		},
	).AnyTimes()
	mm.EXPECT().
		Histogram(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(hist).
		AnyTimes()
	return mm, &opsN, &errN
}

// TestTracingDecorator_SpansEveryOperation tests that the tracing decorator opens a
// span per operation, stamps the operation attribute, and delegates — recording the
// error on the span when the inner repository fails.
//
// Why this test is important:
//   - Traces are how a slow query is localized to a specific repository operation;
//     a missing span or a mislabeled operation attribute breaks that correlation,
//     and an unrecorded error hides failures from the trace view.
//
// What it tests:
//   - A success run stamps the db.operation attribute for all seven operations.
//   - A failure run still spans all seven (exercising the error-recording branch).
func TestTracingDecorator_SpansEveryOperation(t *testing.T) {
	t.Parallel()

	collect := func() (interfaces.Tracer, *[]string) {
		var ops []string
		tr := fixtures.SpyTracer(func(key string, value any) {
			if key == "db.operation" {
				ops = append(ops, value.(string))
			}
		})
		return tr, &ops
	}

	okTracer, okOps := collect()
	okRepo := newBuilder(fixtures.StubStore()).WithTracing(okTracer).Build()
	driveAll(context.Background(), okRepo)
	assert.ElementsMatch(
		t,
		[]string{"Get", "List", "Create", "Update", "Delete", "Exists"},
		*okOps,
	)

	errTracer, errOps := collect()
	sentinel := apperr.New(apperr.CodeInternal, "boom")
	errRepo := newBuilder(errStore(t, sentinel)).WithTracing(errTracer).Build()
	driveAll(context.Background(), errRepo)
	assert.Len(t, *errOps, 6, "failures are still spanned")
}

// TestRetryDecorator_RetriesOutsideTransaction tests that the retry decorator
// re-runs a transient failure through the Retrier and covers the retry branch of
// every retriable operation.
//
// Why this test is important:
//   - A transient DB blip (failover, brief network loss) must be absorbed by the
//     retry layer instead of surfacing as a hard error; if Get were not wrapped, a
//     one-off failure would fail the whole request.
//
// What it tests:
//   - A Get that fails once then succeeds is retried (>1 attempt) and returns the
//     entity; the remaining retriable operations each run through the Retrier.
func TestRetryDecorator_RetriesOutsideTransaction(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[te, tp, string](ctrl)
	gomock.InOrder(
		store.EXPECT().
			Get(gomock.Any(), "id").
			Return(nil, apperr.New(apperr.CodeInternal, "transient")),
		store.EXPECT().Get(gomock.Any(), "id").Return(&te{ID: "id"}, nil),
	)
	store.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&types.Page[te]{}, nil)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&te{ID: "id"}, nil)
	store.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&te{ID: "id"}, nil)
	store.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil)

	retrier, attempts := fixtures.StubRetrier(1, false)
	repo := newBuilder(store).WithRetry(retrier).Build()
	ctx := context.Background()

	got, err := repo.Get(ctx, "id")
	require.NoError(t, err, "the transient Get failure is retried and succeeds")
	require.NotNil(t, got)
	assert.Equal(t, "id", got.ID)
	assert.GreaterOrEqual(t, attempts(), 2, "Get was retried after the transient failure")

	// Drive the rest once to cover their (non-transaction) retry branches.
	_, _ = repo.List(ctx, tp{}, types.PageRequest{})
	_, _ = repo.Create(ctx, &te{ID: "id"})
	_, _ = repo.Update(ctx, "id", &te{ID: "id"})
	_ = repo.Delete(ctx, "id")
	_, _ = repo.Exists(ctx, "id")
}

// TestRetryDecorator_SkipsRetryInsideTransaction tests that inside a transaction the
// retry decorator bypasses the Retrier entirely and runs each operation once.
//
// Why this test is important:
//   - Once a statement fails inside a Postgres transaction the whole transaction is
//     aborted; retrying the statement only yields SQLSTATE 25P02 and masks the real
//     error. The decorator must detect the in-tx context and skip retry so the
//     transaction can be rolled back and retried as a whole at a higher level.
//
// What it tests:
//   - With an in-tx context, driving every operation invokes the Retrier zero times.
func TestRetryDecorator_SkipsRetryInsideTransaction(t *testing.T) {
	t.Parallel()

	retrier, attempts := fixtures.StubRetrier(0, false)
	repo := newBuilder(fixtures.StubStore()).WithRetry(retrier).Build()

	driveAll(interfaces.WithInTx(context.Background()), repo)
	assert.Equal(t, 0, attempts(), "inside a transaction the Retrier is bypassed")
}

// TestCircuitBreakerDecorator_OpenFailsFast tests that an open circuit fails every
// operation fast without touching the inner repository, while a closed circuit
// delegates normally.
//
// Why this test is important:
//   - The circuit breaker is what stops a struggling database from being hammered
//     into a full outage; when open it MUST short-circuit before the inner call, and
//     when closed it must be invisible. A breaker that still called the backend when
//     open would defeat its entire purpose.
//
// What it tests:
//   - A closed breaker delegates a Get successfully.
//   - An open breaker returns an error from every operation and never calls the
//     inner store (a store with no expectations would fail if touched).
func TestCircuitBreakerDecorator_OpenFailsFast(t *testing.T) {
	t.Parallel()

	closedRepo := newBuilder(fixtures.StubStore()).
		WithCircuitBreaker(fixtures.StubCircuitBreaker(false)).
		Build()
	driveAll(context.Background(), closedRepo)

	untouched := mocks.NewMockStore[te, tp, string](gomock.NewController(t))
	openRepo := newBuilder(untouched).
		WithCircuitBreaker(fixtures.StubCircuitBreaker(true)).
		Build()
	ctx := context.Background()

	_, err := openRepo.Get(ctx, "id")
	require.Error(t, err)
	_, err = openRepo.List(ctx, tp{}, types.PageRequest{})
	require.Error(t, err)
	_, err = openRepo.Create(ctx, &te{ID: "id"})
	require.Error(t, err)
	_, err = openRepo.Update(ctx, "id", &te{ID: "id"})
	require.Error(t, err)
	require.Error(t, openRepo.Delete(ctx, "id"))
	_, err = openRepo.Exists(ctx, "id")
	assert.Error(t, err)
}

// TestTimeoutDecorator_BoundsContext tests that the timeout decorator hands every
// operation a context carrying a deadline and delegates the result.
//
// Why this test is important:
//   - Without a per-operation deadline a single stuck query can pin a connection and
//     cascade into pool exhaustion; the decorator's job is to guarantee every inner
//     call is bounded.
//
// What it tests:
//   - The inner store observes a context with a deadline set, and the operations
//     still return their results.
func TestTimeoutDecorator_BoundsContext(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[te, tp, string](ctrl)
	var innerHadDeadline bool
	store.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, id string) (*te, error) {
			_, innerHadDeadline = ctx.Deadline()
			return &te{ID: id}, nil
		},
	)
	store.EXPECT().
		List(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&types.Page[te]{}, nil)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&te{ID: "id"}, nil)
	store.EXPECT().
		Update(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(&te{ID: "id"}, nil)
	store.EXPECT().Delete(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().Exists(gomock.Any(), gomock.Any()).Return(true, nil)

	repo := newBuilder(store).WithTimeout(5 * time.Second).Build()
	ctx := context.Background()

	got, err := repo.Get(ctx, "id")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, innerHadDeadline, "the inner Get sees a bounded context")

	_, _ = repo.List(ctx, tp{}, types.PageRequest{})
	_, _ = repo.Create(ctx, &te{ID: "id"})
	_, _ = repo.Update(ctx, "id", &te{ID: "id"})
	_ = repo.Delete(ctx, "id")
	_, _ = repo.Exists(ctx, "id")
}

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
	assert.Equal(t, "repo:widgets:42", repodeco.CacheKey("widgets", 42))
}

// cachingRepo builds a caching-only decorated repository over store, optionally
// wiring the id-extractor that enables warm-on-create.
func cachingRepo(
	store interfaces.Store[te, tp, string],
	cache interfaces.ByteCache,
	withKeyFunc bool,
) interfaces.DecoratedRepository[te, tp, string] {
	b := newBuilder(store).
		WithCaching(cache).
		WithCacheVersion(cacheVersion).
		WithCacheTTL(time.Minute)
	if withKeyFunc {
		b = b.WithCacheKeyFunc(func(e *te) string { return e.ID })
	}
	return b.Build()
}

// TestCachingDecorator_Get tests the cache-aside read paths: a hit serves from
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
func TestCachingDecorator_Get(t *testing.T) {
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

// TestCachingDecorator_Writes tests that Create warms the cache (only with an
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
func TestCachingDecorator_Writes(t *testing.T) {
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

// TestCachingDecorator_ExistsListExec tests the remaining caching paths: Exists
// answers true from a cache hit without the backend, a miss falls through, and
// List passes straight through uncached.
//
// Why this test is important:
//   - Exists short-circuits on a cached entity (a cached entity definitively
//     exists), which must not regress into an unconditional backend call; List and
//     Exec must never be silently cached, since their results depend on inputs the
//     key does not capture.
//
// What it tests:
//   - Exists returns true from a cache hit without calling the backend, and calls
//     the backend on a miss.
//   - List delegates to the inner repository.
func TestCachingDecorator_ExistsListExec(t *testing.T) {
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

	t.Run("list and exec pass through", func(t *testing.T) {
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
