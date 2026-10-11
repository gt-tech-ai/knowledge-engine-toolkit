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

// readThroughResult is one ReadThrough caller's outcome, sent back from its goroutine.
type readThroughResult struct {
	// err is the error ReadThrough returned.
	err error

	// value is the value ReadThrough returned.
	value int
}

// TestReadThrough_UncacheableResultSharedButNotStored tests that a value the
// WithCacheable predicate rejects reaches every coalesced caller but is never written to
// the cache.
//
// Why this test is important:
//   - A partial result (one upstream section down) must still be served, yet storing it
//     would pin the degraded value for the whole TTL after the upstream recovers.
//
// What it tests:
//   - Two callers both miss on one key (each Get waits until both have arrived) and load
//     a value the predicate rejects; both receive that value with no error, and Set is
//     never called. Whether the two loads coalesce is covered by the nil-cache test.
func TestReadThrough_UncacheableResultSharedButNotStored(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "partial"
	const callers = 2
	const partial = 7

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
	// Count Set calls instead of Times(0): a gomock failure inside a caller goroutine
	// would exit that goroutine before it reports, hanging the test.
	var sets int32
	m.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(context.Context, string, []byte, time.Duration) { atomic.AddInt32(&sets, 1) },
	).
		AnyTimes()

	var sf singleflight.Group
	results := make(chan readThroughResult, callers)
	for range callers {
		go func() {
			v, err := cache.ReadThrough(
				context.Background(), m, &sf, key, time.Minute, 1,
				func() (int, error) { return partial, nil },
				cache.WithCacheable(func(v int) bool { return v != partial }),
			)
			results <- readThroughResult{value: v, err: err}
		}()
	}
	go func() { arrived.Wait(); close(releaseGet) }()
	for range callers {
		r := <-results
		require.NoError(t, r.err)
		assert.Equal(t, partial, r.value, "the rejected value still reaches the caller")
	}
	assert.Equal(t, int32(0), atomic.LoadInt32(&sets), "a rejected value is never stored")
}

// TestReadThrough_NilCacheCoalescesWithoutCaching tests that a nil ByteCache turns
// ReadThrough into a coalesce-only primitive: no cache calls, but one load for concurrent
// callers.
//
// Why this test is important:
//   - A service without a cache backend still needs stampede protection on an expensive
//     read; a nil cache must not panic or force callers to hand-roll their own
//     singleflight path.
//
// What it tests:
//   - Caller A enters the blocked load; caller B then reads the same key; B's load never
//     runs, and both callers receive A's value.
func TestReadThrough_NilCacheCoalescesWithoutCaching(t *testing.T) {
	t.Parallel()
	const key = "nocache"
	var sf singleflight.Group
	var loads int32
	started := make(chan struct{})
	secondLoad := make(chan struct{})
	release := make(chan struct{})
	results := make(chan readThroughResult, 2)

	run := func(load func() (int, error)) {
		v, err := cache.ReadThrough(
			context.Background(),
			nil,
			&sf,
			key,
			time.Minute,
			1,
			load,
		)
		results <- readThroughResult{value: v, err: err}
	}
	go run(func() (int, error) {
		atomic.AddInt32(&loads, 1)
		close(started)
		<-release
		return 42, nil
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first load never started")
	}
	go run(func() (int, error) {
		atomic.AddInt32(&loads, 1)
		close(secondLoad)
		return 99, nil
	})
	// B either attaches to A's flight (its load never runs) or fires its own load.
	// Singleflight exposes no "attached" signal, so give B a generous window to reach the
	// flight before releasing A; the window only costs time when coalescing works.
	select {
	case <-secondLoad:
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	for range 2 {
		r := <-results
		require.NoError(t, r.err)
		assert.Equal(
			t,
			42,
			r.value,
			"every caller receives the single shared load's value",
		)
	}
	assert.Equal(
		t,
		int32(1),
		atomic.LoadInt32(&loads),
		"concurrent callers share one load",
	)
}

// TestReadThrough_FirstCallerCancelDoesNotFailOthers tests that the shared load is
// detached from the first caller: its cancellation returns that caller early without
// failing the others.
//
// Why this test is important:
//   - One impatient client leaving must not abort or poison the load every other
//     coalesced caller is waiting on, and a cancelled caller must not be held until the
//     load finishes.
//
// What it tests:
//   - Caller A starts the blocked load and caller B joins; cancelling A's ctx returns A
//     promptly with a CodeCanceled error; after the load is released B receives its
//     value, and the cache write runs under a context that is not cancelled.
func TestReadThrough_FirstCallerCancelDoesNotFailOthers(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	const key = "detached"
	m.EXPECT().Get(gomock.Any(), key).Return(nil, false).Times(2)
	setCtxErr := make(chan error, 2)
	m.EXPECT().Set(gomock.Any(), key, gomock.Any(), time.Minute).DoAndReturn(
		func(ctx context.Context, _ string, _ []byte, _ time.Duration) {
			setCtxErr <- ctx.Err()
		},
	).MinTimes(1)

	var sf singleflight.Group
	started := make(chan struct{})
	release := make(chan struct{})
	// loader is the shared load's signature; a named type keeps it fixed for the
	// two ReadThrough calls that share it.
	type loader func() (int, error)
	var load loader = func() (int, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return 42, nil
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	resA := make(chan readThroughResult, 1)
	resB := make(chan readThroughResult, 1)
	go func() {
		v, err := cache.ReadThrough(ctxA, m, &sf, key, time.Minute, 1, load)
		resA <- readThroughResult{value: v, err: err}
	}()
	<-started
	go func() {
		v, err := cache.ReadThrough(
			context.Background(),
			m,
			&sf,
			key,
			time.Minute,
			1,
			load,
		)
		resB <- readThroughResult{value: v, err: err}
	}()
	for range 100 {
		runtime.Gosched()
	}
	cancelA()
	select {
	case r := <-resA:
		require.Error(t, r.err)
		assert.Equal(t, apperr.CodeCanceled, apperr.Code(r.err))
	case <-time.After(2 * time.Second):
		t.Error("the cancelled caller was held until the shared load finished")
	}
	close(release)
	r := <-resB
	require.NoError(t, r.err)
	assert.Equal(t, 42, r.value)
	require.NoError(
		t,
		<-setCtxErr,
		"the cache write is not bound to the cancelled caller",
	)
}

// TestReadThrough_LoadPanicBecomesCodedError tests that a panicking load surfaces as a
// coded error to the caller instead of crashing the process.
//
// Why this test is important:
//   - The shared load runs off the caller's goroutine, where an unrecovered panic cannot
//     reach the caller's recovery decorator and would take the whole process down.
//
// What it tests:
//   - A load that panics returns a CodeInternal error, and nothing is cached.
//   - The error's "stack" detail names the panicking function, so the panic is
//     diagnosable.
func TestReadThrough_LoadPanicBecomesCodedError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockByteCache(ctrl)
	m.EXPECT().Get(gomock.Any(), "panic").Return(nil, false)
	m.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)

	var sf singleflight.Group
	_, err := cache.ReadThrough(context.Background(), m, &sf, "panic", time.Minute, 1,
		func() (int, error) { panic("load blew up") })
	require.Error(t, err)
	assert.Equal(t, apperr.CodeInternal, apperr.Code(err))
	var appErr *apperr.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Contains(
		t,
		appErr.Details["stack"],
		"TestReadThrough_LoadPanicBecomesCodedError",
	)
}

// TestReadThrough_DoneContextSkipsLoad tests that a caller whose context has already
// ended gets its coded context error without touching the cache or starting a load.
//
// Why this test is important:
//   - The load is detached from its callers, so a stream of already-expired requests
//     would otherwise each start a load and a cache write that nobody waits for.
//
// What it tests:
//   - A cancelled ctx returns CodeCanceled and an expired one CodeTimeout; Get, Set and
//     the load never run.
func TestReadThrough_DoneContextSkipsLoad(t *testing.T) {
	t.Parallel()
	m := mocks.NewMockByteCache(gomock.NewController(t))
	var sf singleflight.Group
	var loads int32
	load := func() (int, error) { atomic.AddInt32(&loads, 1); return 1, nil }

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cache.ReadThrough(canceled, m, &sf, "done", time.Minute, 1, load)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeCanceled, apperr.Code(err))

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()
	_, err = cache.ReadThrough(expired, m, &sf, "done", time.Minute, 1, load)
	require.Error(t, err)
	assert.Equal(t, apperr.CodeTimeout, apperr.Code(err))
	assert.Equal(t, int32(0), atomic.LoadInt32(&loads), "a done caller starts no load")
}
