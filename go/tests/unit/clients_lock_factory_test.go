package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestLockFactory_LocalBackend tests that NewFromConfig builds a working
// in-process lock when kind=local.
//
// Why this test is important:
//   - Selecting the backend by config (not a compile-time import) is the One
//     Idea; dev/single-replica must get the local backend and it must actually
//     lock.
//
// What it tests:
//   - NewFromConfig(kind=local) returns a non-nil lock whose Acquire grants a key.
func TestLockFactory_LocalBackend(t *testing.T) {
	t.Parallel()
	l, err := lock.NewFromConfig(t.Context(), lock.Config{
		Kind:          lock.KindLocal,
		TTL:           30 * time.Second,
		RenewInterval: 10 * time.Second,
	}, lock.Deps{})
	require.NoError(t, err)
	require.NotNil(t, l)

	// Acquire (promoted from the embedded DistributedLock) grants a free key.
	tok, ok, err := l.Acquire(context.Background(), "k")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.NotEmpty(t, tok)

	// Hold (the Locker method) acquires + manages a distinct key — proving the
	// factory returns a fully wired Locker, not just a primitive backend.
	heldCtx, release, held, err := l.Hold(context.Background(), "k2")
	require.NoError(t, err)
	assert.True(t, held, "Hold on a free key should acquire")
	require.NoError(t, heldCtx.Err(), "held context is live while held")
	release()
}

// TestLockFactory_Validation tests that NewFromConfig fails loudly on an unknown
// kind, an out-of-range renew interval, and a redis kind with no client.
//
// Why this test is important:
//   - A misconfigured lock must fail at construction, not silently at runtime.
//     renew_interval must stay well under the TTL (≤ ttl/3) so a couple of
//     missed renew ticks don't expire a live lease; redis with no injected
//     client is a wiring bug the factory should catch.
//
// What it tests:
//   - Unknown kind → error; renew_interval > ttl/3 → error; kind=redis with a
//     nil client → error.
func TestLockFactory_Validation(t *testing.T) {
	t.Parallel()

	_, err := lock.NewFromConfig(t.Context(), lock.Config{
		Kind: "bogus", TTL: time.Second, RenewInterval: 100 * time.Millisecond,
	}, lock.Deps{})
	require.Error(t, err, "unknown kind must error")

	_, err = lock.NewFromConfig(t.Context(), lock.Config{
		Kind: lock.KindLocal, TTL: 30 * time.Second, RenewInterval: 20 * time.Second,
	}, lock.Deps{})
	require.Error(t, err, "renew_interval > ttl/3 must error")

	_, err = lock.NewFromConfig(t.Context(), lock.Config{
		Kind: lock.KindRedis, TTL: 30 * time.Second, RenewInterval: 10 * time.Second,
	}, lock.Deps{})
	require.Error(t, err, "kind=redis with a nil client must error")
}

// TestHold_AcquiresThenReleaseCancels tests the happy path: Hold acquires and
// returns a live context, and release cancels it and frees the lock.
//
// Why this test is important:
//   - This is the seam the caller consumes: acquire → run under held → release.
//     release must both cancel the held context and release the underlying lock,
//     or a finished operation would leave its resource locked.
//
// What it tests:
//   - Hold returns acquired=true and an un-cancelled held context; after
//     release, held is cancelled and the lock's Release was called with the token.
func TestHold_AcquiresThenReleaseCancels(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockDistributedLock(ctrl)
	m.EXPECT().Acquire(gomock.Any(), "k").Return("tok", true, nil)
	m.EXPECT().Release(gomock.Any(), "k", "tok").Return(nil).Times(1)

	// A long renew interval keeps the watchdog quiet for the test's duration.
	held, release, ok, err := lock.NewLocker(m, time.Hour).Hold(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, held)
	require.NoError(t, held.Err(), "held context should be live while the lock is held")

	release()
	assert.Error(t, held.Err(), "release must cancel the held context")
}

// TestHold_NotAcquired tests that a losing Hold reports acquired=false and does
// not release a lock it never took.
//
// Why this test is important:
//   - If the key is already held, the caller rejects the operation as already
//     in progress; Hold must signal that without touching the lock owned by
//     another pod.
//
// What it tests:
//   - When Acquire returns acquired=false, Hold returns acquired=false, nil, and
//     the returned release is a safe no-op (no Release call on the mock).
func TestHold_NotAcquired(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockDistributedLock(ctrl)
	m.EXPECT().Acquire(gomock.Any(), "k").Return("", false, nil)

	_, release, ok, err := lock.NewLocker(m, time.Hour).Hold(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok, "a contended key must report acquired=false")

	release() // must not call Release (no EXPECT registered)
}

// TestHold_RenewsPeriodicallyWhileHeld tests that Hold's watchdog keeps the lease
// alive by renewing on its interval, and that the held context stays live until
// release.
//
// Why this test is important:
//   - An operation can outlast the base TTL; the watchdog's periodic Renew is what
//     keeps the lock held meanwhile. If Hold never renewed, a long operation would
//     lose its lock mid-flight and a second pod could start a competing one.
//
// What it tests:
//   - With a short interval, Renew is called at least once and the held context
//     stays live; after release the context is cancelled and the lock is freed.
func TestHold_RenewsPeriodicallyWhileHeld(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockDistributedLock(ctrl)
	m.EXPECT().Acquire(gomock.Any(), "k").Return("tok", true, nil)
	m.EXPECT().Renew(gomock.Any(), "k", "tok").Return(true, nil).MinTimes(1)
	m.EXPECT().Release(gomock.Any(), "k", "tok").Return(nil).Times(1)

	held, release, ok, err := lock.NewLocker(m, 15*time.Millisecond).
		Hold(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, ok)

	// Allow the watchdog to tick and renew a few times (timing behaviour under test).
	time.Sleep(70 * time.Millisecond)
	require.NoError(t, held.Err(), "successful renewals keep the held context live")

	release()
	assert.Error(t, held.Err(), "release cancels the held context")
}

// TestHold_LeaseLostCancelsHeld tests the correctness guarantee at the heart of
// the single-active-operation guard: if the watchdog's Renew reports the lease
// lost, the held context is cancelled so the in-flight operation stops.
//
// Why this test is important:
//   - Without this, a pod whose lease expired (or was taken over) would keep
//     running the guarded operation while a second pod runs a competing one —
//     two active operations, the exact failure the guard prevents.
//
// What it tests:
//   - With a short renew interval and a mock Renew that returns held=false, the
//     held context becomes cancelled without release being called.
func TestHold_LeaseLostCancelsHeld(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	m := mocks.NewMockDistributedLock(ctrl)
	m.EXPECT().Acquire(gomock.Any(), "k").Return("tok", true, nil)
	m.EXPECT().Renew(gomock.Any(), "k", "tok").Return(false, nil)
	// release() is still invoked by the deferred cleanup; Release is a no-op here.
	m.EXPECT().Release(gomock.Any(), "k", "tok").Return(nil).AnyTimes()

	held, release, ok, err := lock.NewLocker(m, 10*time.Millisecond).
		Hold(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, ok)
	defer release()

	select {
	case <-held.Done():
		// expected: lease loss cancelled the held context
	case <-time.After(2 * time.Second):
		t.Fatal("held context was not cancelled after the lease was lost")
	}
}
