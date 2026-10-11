package unit_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/local"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// TestLocalLock_Acquire_Contention tests that the in-process backend grants a
// key to exactly one holder until it is released.
//
// Why this test is important:
//   - The whole point of the lock is single-holder exclusion; if a second
//     Acquire on a held key also "won", two replicas would run the same
//     guarded operation concurrently — the exact failure the lock prevents.
//
// What it tests:
//   - The first Acquire returns a non-empty token + acquired=true; a second
//     Acquire on the same key returns acquired=false with an empty token.
func TestLocalLock_Acquire_Contention(t *testing.T) {
	t.Parallel()
	l := local.New()
	ctx := context.Background()

	tok, ok, err := l.Acquire(ctx, "resource:1:lock")
	require.NoError(t, err)
	require.True(t, ok, "first Acquire should win")
	require.NotEmpty(t, tok, "winner should receive a fencing token")

	tok2, ok2, err := l.Acquire(ctx, "resource:1:lock")
	require.NoError(t, err)
	assert.False(t, ok2, "second Acquire on a held key must lose")
	assert.Empty(t, tok2, "loser must not receive a token")
}

// TestLocalLock_Release_TokenFenced tests that only the holder's token frees a
// key — a foreign token is a no-op.
//
// Why this test is important:
//   - Fencing prevents a stale/previous holder from releasing a lock a new
//     holder now owns. Without it, a late Release from holder A could free
//     holder B's active lock and admit a third operation.
//
// What it tests:
//   - Release with a foreign token leaves the key held; Release with the
//     holder's token frees it so the next Acquire wins.
func TestLocalLock_Release_TokenFenced(t *testing.T) {
	t.Parallel()
	l := local.New()
	ctx := context.Background()

	tok, ok, err := l.Acquire(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, l.Release(ctx, "k", "not-the-token"))
	_, ok2, err := l.Acquire(ctx, "k")
	require.NoError(t, err)
	assert.False(t, ok2, "foreign-token Release must not free the lock")

	require.NoError(t, l.Release(ctx, "k", tok))
	_, ok3, err := l.Acquire(ctx, "k")
	require.NoError(t, err)
	assert.True(t, ok3, "holder-token Release frees the lock")
}

// TestLocalLock_Renew tests the Renew contract: held iff the token still owns
// the key.
//
// Why this test is important:
//   - Hold's watchdog calls Renew to keep a lease alive and treats held=false
//     as "lease lost" (it then cancels the in-flight operation). A wrong Renew
//     result either drops a valid lock or hides a lost one.
//
// What it tests:
//   - Correct token → held=true; foreign token → held=false; after Release →
//     held=false; unknown key → held=false. Never an error on the local backend.
func TestLocalLock_Renew(t *testing.T) {
	t.Parallel()
	l := local.New()
	ctx := context.Background()

	tok, ok, err := l.Acquire(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)

	held, err := l.Renew(ctx, "k", tok)
	require.NoError(t, err)
	assert.True(t, held, "current holder's token should keep the lease")

	held, err = l.Renew(ctx, "k", "foreign")
	require.NoError(t, err)
	assert.False(t, held, "foreign token must not renew")

	require.NoError(t, l.Release(ctx, "k", tok))
	held, err = l.Renew(ctx, "k", tok)
	require.NoError(t, err)
	assert.False(t, held, "a released key cannot be renewed")

	held, err = l.Renew(ctx, "never-acquired", "x")
	require.NoError(t, err)
	assert.False(t, held, "an unknown key cannot be renewed")
}

// TestLocalLock_DistinctKeys_Independent tests that different keys never
// contend and each Acquire mints a distinct token.
//
// Why this test is important:
//   - Locks are keyed per resource; two different resources must both
//     acquire, and their tokens must differ so one's Release can never free the
//     other.
//
// What it tests:
//   - Acquire on two different keys both succeed; the two tokens are distinct.
func TestLocalLock_DistinctKeys_Independent(t *testing.T) {
	t.Parallel()
	l := local.New()
	ctx := context.Background()

	tokA, okA, err := l.Acquire(ctx, "resource:a:lock")
	require.NoError(t, err)
	tokB, okB, err := l.Acquire(ctx, "resource:b:lock")
	require.NoError(t, err)

	assert.True(t, okA, "key a should acquire")
	assert.True(t, okB, "key b should acquire independently of a")
	assert.NotEqual(t, tokA, tokB, "each holder must get a distinct fencing token")
}

// TestLocalLock_Acquire_ConcurrentSingleWinner tests that under a race of many
// goroutines on one key, exactly one Acquire wins.
//
// Why this test is important:
//   - The local backend guards its held-key map with a mutex; a data race or a
//     check-then-set gap would let two goroutines both "win", silently breaking
//     exclusion under real concurrency (the -race build would also flag it).
//
// What it tests:
//   - With N goroutines racing one key, the count of acquired=true results is
//     exactly one.
func TestLocalLock_Acquire_ConcurrentSingleWinner(t *testing.T) {
	t.Parallel()
	l := local.New()
	ctx := context.Background()

	const n = 64
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			<-start
			if _, ok, err := l.Acquire(ctx, "hot"); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, int64(1), wins.Load(), "exactly one goroutine may hold the key")
}

// compile-time assertion that the local backend satisfies the seam.
var _ interfaces.DistributedLock = local.New()
