package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/decoration"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/config/schema/resilience"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/repos/repository"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// reposEnt and reposEntParams are minimal entity/param types for the decorated-repo test.
type reposEnt struct{ ID string }

type reposEntParams struct{}

// repoDecorations returns the default decoration config with the "test" repository's
// retry tuned to millisecond backoff, observed through no-op collaborators.
func repoDecorations(timeout time.Duration) decorate.Decorations {
	cfg := decoration.DefaultConfig()
	cfg.Repositories.Overrides = map[string]decoration.Layers{"test": {
		Timeout: &timeout,
		Retry: decoration.Retry{RetryConfig: resilience.RetryConfig{
			InitialInterval: time.Millisecond,
			MaxInterval:     2 * time.Millisecond,
		}},
	}}
	return decorate.NewDecorations(&cfg, decorate.Collaborators{
		Observability: decorate.Observability{
			Logger:  fixtures.NopLogger(),
			Metrics: fixtures.NopMetrics(),
			Tracer:  fixtures.NopTracer(),
		},
	})
}

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
//   - Under the default decoration config (retry on at the repository tier), a Get
//     whose store fails transiently once then succeeds is retried by the stack and
//     returns the store's entity — proving the helper builds its layers from the
//     repositories tier and delegates to the base store it wraps.
func TestDecoratedFromConfig_WiresStackAndDelegates(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore[reposEnt, reposEntParams, string](ctrl)

	want := &reposEnt{ID: "abc"}
	gomock.InOrder(
		store.EXPECT().Get(gomock.Any(), "abc").Return(nil, apperr.Sentinel("transient")),
		store.EXPECT().Get(gomock.Any(), "abc").Return(want, nil),
	)

	repo, err := repository.DecoratedFromConfig(store, "test",
		repository.Settings{},
		repository.Deps[reposEnt, string]{Decorations: repoDecorations(time.Second)})
	require.NoError(t, err)

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

	// A zero timeout → no timeout layer; a cache + key func are supplied.
	repo, err := repository.DecoratedFromConfig(store, "test",
		repository.Settings{CacheTTL: time.Minute, CacheVersion: 1},
		repository.Deps[reposEnt, string]{
			Decorations: repoDecorations(0),
			Cache:       cache,
			IDOf:        func(e *reposEnt) string { return e.ID },
		})
	require.NoError(t, err)

	got, err := repo.Get(context.Background(), "xyz")
	require.NoError(t, err)
	assert.Equal(
		t,
		want,
		got,
		"the entity flows through the wired caching layer to the store",
	)
}
