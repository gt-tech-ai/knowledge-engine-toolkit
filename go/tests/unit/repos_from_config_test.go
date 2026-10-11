package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/circuitbreaker"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/retry"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// reposEnt and reposEntParams are minimal entity/param types for the decorated-repo test.
type reposEnt struct{ ID string }

type reposEntParams struct{}

// TestDecoratedFromConfig_WiresStackAndDelegates tests that the shared
// DecoratedFromConfig helper assembles the resilience +
// observability stack around a base store and delegates through it.
//
// Why this test is important:
//   - DecoratedFromConfig is the single decorated-repo assembly every service now
//     injects. If it dropped the Retry layer or failed to wrap the base store, a
//     transient DB blip would surface as a hard error and every repository across
//     the fleet would silently lose its resilience.
//
// What it tests:
//   - A Get whose store fails transiently once then succeeds is retried by the
//     stack and returns the store's entity — proving the helper wires the Retry
//     decorator and delegates to the base store it wraps.
func TestDecoratedFromConfig_WiresStackAndDelegates(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[reposEnt, reposEntParams, string](ctrl)

	want := &reposEnt{ID: "abc"}
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), "abc").Return(nil, apperr.Sentinel("transient")),
		store.EXPECT().Get(gomock.Any(), "abc").Return(want, nil),
	)

	retrier, err := retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      3,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  time.Second,
	})
	require.NoError(t, err)
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "test.repo")
	require.NoError(t, err)

	repo := repository.DecoratedFromConfig(store, "test",
		repository.Settings{Timeout: time.Second},
		repository.Deps[reposEnt, string]{
			Logger:  fixtures.NopLogger(),
			Metrics: fixtures.NopMetrics(),
			Tracer:  fixtures.NopTracer(),
			Retrier: retrier,
			Breaker: cb,
		})

	got, err := repo.Get(context.Background(), "abc")
	require.NoError(
		t,
		err,
		"the transient Get failure should be retried by the wired Retry layer",
	)
	assert.Equal(
		t,
		want,
		got,
		"the base store's entity flows back through the decorated stack",
	)
}

// TestDecoratedFromConfig_WiresCachingLayer tests that supplying a cache wires the
// caching decorator (with a key func) and no timeout layer when Timeout is zero.
//
// Why this test is important:
//   - Caching is the one optional layer the helper adds only when a cache is
//     supplied; if the Cache/IDOf branch were mis-wired, a cache-backed repository
//     would either bypass the cache or panic on a nil key func.
//
// What it tests:
//   - With a cache (returning a miss) and an IDOf key func, a Get flows through the
//     caching layer to the store and returns the entity — exercising the caching
//     and zero-timeout branches.
func TestDecoratedFromConfig_WiresCachingLayer(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[reposEnt, reposEntParams, string](ctrl)
	cache := mocks.NewMockByteCache(ctrl)

	want := &reposEnt{ID: "xyz"}
	cache.EXPECT().Get(gomock.Any(), gomock.Any()).Return(nil, false).AnyTimes()
	cache.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	store.EXPECT().Get(gomock.Any(), "xyz").Return(want, nil)

	retrier, err := retry.NewFromConfig(retry.Config{
		Kind:            retry.KindExponential,
		MaxRetries:      1,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2.0,
		MaxElapsedTime:  time.Second,
	})
	require.NoError(t, err)
	cb, err := circuitbreaker.New(circuitbreaker.KindGoBreaker, "test.cache")
	require.NoError(t, err)

	// Timeout omitted (zero) → no timeout layer; a cache + key func are supplied.
	repo := repository.DecoratedFromConfig(store, "test",
		repository.Settings{CacheTTL: time.Minute, CacheVersion: 1},
		repository.Deps[reposEnt, string]{
			Logger:  fixtures.NopLogger(),
			Metrics: fixtures.NopMetrics(),
			Tracer:  fixtures.NopTracer(),
			Retrier: retrier,
			Breaker: cb,
			Cache:   cache,
			IDOf:    func(e *reposEnt) string { return e.ID },
		})

	got, err := repo.Get(context.Background(), "xyz")
	require.NoError(t, err)
	assert.Equal(
		t,
		want,
		got,
		"the entity flows through the wired caching layer to the store",
	)
}
