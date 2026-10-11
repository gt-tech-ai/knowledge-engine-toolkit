package unit_test

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/sync/singleflight"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestReadThrough_SingleFlightCollapsesConcurrent tests that concurrent cache misses on
// the same key collapse via single-flight, so a cold key under a request stampede hits
// the backend far fewer times than the number of callers — not once per caller.
//
// Why this test is important:
//   - The whole point of caching a live DISTINCT / COUNT is to avoid recomputing it;
//     without single-flight, a burst of simultaneous misses (e.g. every keystroke from
//     many users on a hot prefix) would each fire the expensive backend load, defeating
//     the cache exactly when it's needed most. This proves the collapse holds.
//
// What it tests:
//   - 20 goroutines rendezvous at the pre-Do barrier and call ReadThrough with the same
//     key while the load is blocked; the load runs at least once but strictly fewer than
//     20 times (the stampede is suppressed), and every caller receives the loaded result.
//     Exactly-once is not asserted — see the assertion comment for why it is inherently
//     racy for a black-box goroutine stampede.
func TestReadThrough_SingleFlightCollapsesConcurrent(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "k1"
	const ttl = time.Minute
	const callers = 20

	// Rendezvous barrier INSIDE Get — the call ReadThrough makes immediately before
	// singleflight.Do. Every caller blocks here until all `callers` have arrived, then
	// they are released together, so each reaches Do while the winner's load is still
	// in-flight and collapses onto it. This removes the race the previous version had: it
	// gated `release` on a counter incremented in the goroutine BODY (before ReadThrough
	// even called Get), so a straggler could still be between that counter and Do when
	// the winner finished — and, with an always-miss mock, that straggler fired a SECOND
	// load (flaky "expected 1, actual 2"). The winner still Sets once (AnyTimes covers
	// the collapsed single Set).
	var arrived sync.WaitGroup
	arrived.Add(callers)
	releaseGet := make(chan struct{})
	m.EXPECT().Get(gomock.Any(), key).DoAndReturn(
		func(context.Context, string) ([]byte, bool) {
			arrived.Done()
			<-releaseGet
			return nil, false
		},
	).Times(callers)
	m.EXPECT().Set(gomock.Any(), key, gomock.Any(), ttl).AnyTimes()

	var sf singleflight.Group
	var loadCount int32
	release := make(
		chan struct{},
	) // the single winner's load blocks here so all callers pile onto one flight
	started := make(
		chan struct{},
	) // the winner closes this once it is confirmed in-flight
	var startOnce sync.Once
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := cache.ReadThrough(
				context.Background(), m, &sf, key, ttl, 1,
				func() (int, error) {
					atomic.AddInt32(&loadCount, 1)
					startOnce.Do(
						func() { close(started) },
					) // the winner signals it is in-flight
					<-release
					return 42, nil
				},
			)
			assert.NoError(t, err)
			assert.Equal(t, 42, v)
		}()
	}
	// Open the Get barrier once every caller has reached it, so all callers proceed to Do
	// together.
	go func() { arrived.Wait(); close(releaseGet) }()

	// The winner cannot signal `started` until it has cleared the Get barrier — which
	// opens only after ALL callers arrived — so `started` firing proves every caller has
	// left Get and is entering Do with the winner in-flight (singleflight attaches a
	// concurrent Do to the running call via a mutex + map, no yield points). Yield
	// generously to let the non-winners attach, then release the winner.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the single-flight winner never entered its load")
	}
	for range 100 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	// Assert the collapse HAPPENED — at least one load, but fewer than the caller count —
	// rather than exactly one. Exactly-one is not deterministically achievable for a
	// black-box goroutine stampede: a caller can always be preempted in the few
	// instructions between Get returning and singleflight.Do acquiring the in-flight
	// entry, so if the winner finishes first that straggler fires its own load. This is
	// the same bound `x/sync/singleflight`'s own TestDoDupSuppress asserts (`0 < calls <
	// n`). The Get barrier + block-until-attached above make the realistic result 1
	// (occasionally a small number under load); the assertion tolerates that rare
	// straggler without flaking.
	loads := atomic.LoadInt32(&loadCount)
	assert.GreaterOrEqual(t, loads, int32(1), "at least one caller must load")
	assert.Less(t, loads, int32(callers),
		"single-flight suppresses the stampede — far fewer loads than callers")
}

// TestReadThrough_HitSkipsLoad tests that a version-matching cache hit returns the
// decoded value without calling load — the read-through fast path.
//
// Why this test is important:
//   - A hit that still called the backend would make the cache pure overhead. This pins
//     that a hit is served entirely from the cache.
//
// What it tests:
//   - with the ByteCache returning a valid encoded envelope, ReadThrough returns the
//     decoded value and never invokes load.
func TestReadThrough_HitSkipsLoad(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "k2"
	encoded, err := cache.Encode(99, 1)
	require.NoError(t, err)
	m.EXPECT().Get(gomock.Any(), key).Return(encoded, true)

	var sf singleflight.Group
	loadCalled := false
	v, err := cache.ReadThrough(
		context.Background(),
		m,
		&sf,
		key,
		time.Minute,
		1,
		func() (int, error) {
			loadCalled = true
			return 0, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, 99, v, "the cached value is returned")
	assert.False(t, loadCalled, "a hit does not call load")
}

// TestReadThrough_LoadErrorPropagatesUncached tests that when the load fails, the error
// propagates and nothing is cached.
//
// Why this test is important:
//   - Caching a load error would serve a transient failure to every later caller for the
//     TTL. A load error must propagate to THIS caller and leave the cache untouched, so
//     the next request retries.
//
// What it tests:
//   - Get miss + load returns an error → the error propagates and Set is never called.
func TestReadThrough_LoadErrorPropagatesUncached(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "kerr"
	errLoad := apperr.Sentinel("backend load failed")
	m.EXPECT().Get(gomock.Any(), key).Return(nil, false)
	m.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	var sf singleflight.Group
	_, err := cache.ReadThrough(
		context.Background(),
		m,
		&sf,
		key,
		time.Minute,
		1,
		func() (int, error) {
			return 0, errLoad
		},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, errLoad, "the load error propagates unchanged")
}

// TestReadThrough_StaleVersionReloads tests that a cached envelope encoded at an older
// version is treated as a miss: load re-runs and its fresh value is returned (and
// re-cached at the new version).
//
// Why this test is important:
//   - The version stamp is how a shape change invalidates every prior entry (a bumped
//     version must not deserialize an old envelope into the new type). A stale-version
//     hit must reload, not return the old value.
//
// What it tests:
//   - Get returns an envelope encoded at version-1 → load runs and its value is returned.
func TestReadThrough_StaleVersionReloads(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "kstale"
	const version = 2
	stale, err := cache.Encode(1, version-1) // encoded at the PREVIOUS version
	require.NoError(t, err)
	m.EXPECT().Get(gomock.Any(), key).Return(stale, true)
	m.EXPECT().
		Set(gomock.Any(), key, gomock.Any(), gomock.Any())
		// the fresh value is re-cached

	var sf singleflight.Group
	loaded := false
	v, err := cache.ReadThrough(
		context.Background(),
		m,
		&sf,
		key,
		time.Minute,
		version,
		func() (int, error) {
			loaded = true
			return 77, nil
		},
	)
	require.NoError(t, err)
	assert.True(t, loaded, "a stale-version hit re-runs load")
	assert.Equal(t, 77, v, "the fresh value is returned, not the stale-version entry")
}
