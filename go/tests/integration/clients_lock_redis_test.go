//go:build integration

// Package integration verifies the Redis DistributedLock backend against a real
// Redis instance (redis:7-alpine via the shared RedisIntegrationSuite fixture).
// The lock is built over the live go-redis client reused from the cache
// (rediscache.Cache.Client()) — the exact seam the caller wires against — so these
// tests exercise real SET NX PX acquisition and the Lua compare-then-act
// renew/release that no mock or in-memory substitute can validate.
package integration

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	rediscache "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock"
	lockredis "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/lock/redis"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	testsuite "github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures/suite"
)

// RedisLockSuite holds a real Redis container and a cache client whose
// underlying go-redis connection backs the lock under test.
type RedisLockSuite struct {
	cache *rediscache.Cache
	testsuite.RedisIntegrationSuite
}

// TestRedisLockSuite runs all Redis DistributedLock integration tests.
//
// Why this test is important:
//   - Without a top-level TestXxx, `go test` ignores every suite method.
//
// What it tests:
//   - Suite entrypoint wires RedisLockSuite into the testify runner.
func TestRedisLockSuite(t *testing.T) {
	suite.Run(t, new(RedisLockSuite))
}

func (s *RedisLockSuite) SetupTest() {
	cfg := rediscache.DefaultConfig()
	cfg.Addr = s.RedisAddr
	cfg.OpTimeout = 2 * time.Second
	s.cache = rediscache.New(&cfg)
}

func (s *RedisLockSuite) TearDownTest() {
	if s.cache != nil {
		_ = s.cache.Close()
	}
}

// newLock builds a lock over the reused go-redis client with the given lease TTL.
func (s *RedisLockSuite) newLock(ttl time.Duration) interfaces.DistributedLock {
	return lockredis.New(s.cache.Client(), ttl)
}

// key returns a test-scoped lock key so tests sharing one Redis never collide.
func (s *RedisLockSuite) key(label string) string {
	return "test:" + s.T().Name() + ":" + label
}

// TestAcquire_SingleWinner tests that of two contenders on one key, exactly one
// acquires.
//
// Why this test is important:
//   - Cross-pod exclusion is the feature's reason to exist; if both pods'
//     Acquire "won", two replicas would run the same guarded operation.
//
// What it tests:
//   - First Acquire wins with a non-empty token; the second on the same key
//     returns acquired=false; after the winner releases, a new Acquire wins.
func (s *RedisLockSuite) TestAcquire_SingleWinner() {
	ctx := context.Background()
	lk := s.newLock(30 * time.Second)
	k := s.key("winner")

	tok, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok, "first Acquire should win")
	s.Require().NotEmpty(tok)

	_, ok2, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.False(ok2, "second Acquire on a held key must lose")

	s.Require().NoError(lk.Release(ctx, k, tok))
	_, ok3, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.True(ok3, "after Release the key is free again")
}

// TestAcquire_ConcurrentSingleWinner tests that under N goroutines racing one
// key, exactly one wins — leaning on Redis SET NX atomicity, not app locking.
//
// Why this test is important:
//   - The real deployment races many pods; a check-then-set gap would admit two
//     holders. Only a real Redis SET NX proves the atomicity holds under load.
//
// What it tests:
//   - With N concurrent Acquire calls on one key, the acquired=true count is 1.
func (s *RedisLockSuite) TestAcquire_ConcurrentSingleWinner() {
	ctx := context.Background()
	lk := s.newLock(30 * time.Second)
	k := s.key("hot")

	const n = 32
	var wins atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			<-start
			if _, ok, err := lk.Acquire(ctx, k); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	s.Equal(int64(1), wins.Load(), "exactly one contender may hold the key")
}

// TestAcquire_TTLExpiryFreesCrashedHolder tests that a holder that never
// releases (a crashed pod) loses the key when the lease TTL elapses.
//
// Why this test is important:
//   - TTL auto-expiry is the only thing that frees a lock held by a dead pod;
//     without it a crash would wedge the resource forever. A mock can't prove
//     real Redis expiry.
//
// What it tests:
//   - After Acquire with a short TTL and no Release, a later Acquire eventually
//     succeeds once the lease expires.
func (s *RedisLockSuite) TestAcquire_TTLExpiryFreesCrashedHolder() {
	ctx := context.Background()
	lk := s.newLock(150 * time.Millisecond)
	k := s.key("crashed")

	_, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok, "crashed holder acquires first")
	// Intentionally never release: simulate a crash.

	s.Require().Eventually(func() bool {
		_, ok, err := lk.Acquire(ctx, k)
		return err == nil && ok
	}, 2*time.Second, 20*time.Millisecond, "lease should expire and free the key")
}

// TestRelease_TokenFencedOnLiveKey tests that a foreign token cannot release a
// key currently held by another token.
//
// Why this test is important:
//   - Fencing is what stops a slow/previous holder from freeing a lock a new
//     holder now owns and admitting a third operation.
//
// What it tests:
//   - Release with a foreign token leaves the key held; the holder's token frees it.
func (s *RedisLockSuite) TestRelease_TokenFencedOnLiveKey() {
	ctx := context.Background()
	lk := s.newLock(30 * time.Second)
	k := s.key("fenced")

	tok, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok)

	s.Require().NoError(lk.Release(ctx, k, "someone-elses-token"))
	_, ok2, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.False(ok2, "foreign-token Release must not free a live lock")

	s.Require().NoError(lk.Release(ctx, k, tok))
	_, ok3, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.True(ok3, "holder-token Release frees the lock")
}

// TestRelease_StaleTokenAfterExpiry_ABA tests the compare-then-DEL guard: a
// stale Release from a holder whose lease already expired must NOT delete the
// new holder's lock on the same key.
//
// Why this test is important:
//   - This is the raison d'être of compare-then-DEL over a bare DEL. A→expire,
//     B→acquire, then A's late Release must be a no-op; a bare DEL would free
//     B's live lock and admit a duplicate operation — the ABA hazard.
//
// What it tests:
//   - After A's lease expires and B re-acquires a new token, A's Release with
//     its old token leaves B's lock intact (a third Acquire still loses).
func (s *RedisLockSuite) TestRelease_StaleTokenAfterExpiry_ABA() {
	ctx := context.Background()
	lk := s.newLock(150 * time.Millisecond)
	k := s.key("aba")

	tokA, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok, "holder A acquires")

	// Wait for A's lease to expire, then B acquires a fresh token on the same key.
	var tokB string
	s.Require().Eventually(func() bool {
		t, ok, err := lk.Acquire(ctx, k)
		if err == nil && ok {
			tokB = t
			return true
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "B should acquire after A's lease expires")
	s.Require().NotEqual(tokA, tokB)

	// A's stale Release must not free B's live lock.
	s.Require().NoError(lk.Release(ctx, k, tokA))
	_, ok3, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.False(ok3, "A's stale Release must not delete B's live lock")

	held, err := lk.Renew(ctx, k, tokB)
	s.Require().NoError(err)
	s.True(held, "B should still hold the lock after A's stale Release")
}

// TestRenew_AfterExpiryReportsLost tests that Renew on a key whose lease has
// expired reports held=false without error.
//
// Why this test is important:
//   - Hold's watchdog treats held=false as "lease lost" and cancels the operation.
//     A false-positive Renew after expiry would let a pod keep working under a
//     lock it no longer owns.
//
// What it tests:
//   - After the lease expires, Renew with the original token returns held=false, nil.
func (s *RedisLockSuite) TestRenew_AfterExpiryReportsLost() {
	ctx := context.Background()
	ttl := 120 * time.Millisecond
	lk := s.newLock(ttl)
	k := s.key("renew-lost")

	tok, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok)

	// This asserts actual TTL-expiry timing, so we sleep past the lease rather
	// than poll: polling Renew would defeat the test, since a successful Renew
	// extends the lease and the key would never expire. Sleep well past the TTL,
	// then Renew exactly once.
	time.Sleep(ttl + 130*time.Millisecond)

	held, err := lk.Renew(ctx, k, tok)
	s.Require().NoError(err)
	s.False(held, "Renew must report the lease lost after expiry")
}

// TestRenew_KeepsHoldAlive tests that Renew extends the lease so a long hold
// outlives the base TTL.
//
// Why this test is important:
//   - An operation can outlast the base TTL; the watchdog's periodic Renew is what
//     keeps the lock held meanwhile. If Renew didn't extend, the lock would
//     expire mid-operation and admit a duplicate.
//
// What it tests:
//   - With Renew called before expiry, the key stays held past the base TTL (a
//     contender still loses), and a final Renew still reports held=true.
func (s *RedisLockSuite) TestRenew_KeepsHoldAlive() {
	ctx := context.Background()
	ttl := 300 * time.Millisecond
	lk := s.newLock(ttl)
	k := s.key("renew-alive")

	tok, ok, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok)

	// Renew before expiry, twice, spanning more than the base TTL in total.
	for range 2 {
		time.Sleep(ttl * 2 / 3)
		held, err := lk.Renew(ctx, k, tok)
		s.Require().NoError(err)
		s.Require().True(held, "Renew before expiry keeps the lease")
	}

	_, contender, err := lk.Acquire(ctx, k)
	s.Require().NoError(err)
	s.False(contender, "the renewed key is still held past its base TTL")
}

// TestNewFromConfig_RedisBackend tests that the config factory wires the redis
// backend over the reused cache client and produces a working cross-pod lock.
//
// Why this test is important:
//   - The caller builds the lock through NewFromConfig(kind=redis), not by
//     constructing the backend directly. This exercises that exact path over the
//     live Cache.Client() seam, so the factory's redis branch and the client
//     reuse are proven together.
//
// What it tests:
//   - NewFromConfig(kind=redis, cacheClient) returns a Locker whose Acquire
//     contends and whose Hold acquires + manages a key against real Redis.
func (s *RedisLockSuite) TestNewFromConfig_RedisBackend() {
	ctx := context.Background()
	l, err := lock.NewFromConfig(&lock.Config{
		Kind:          lock.KindRedis,
		TTL:           30 * time.Second,
		RenewInterval: 10 * time.Second,
	}, s.cache.Client())
	s.Require().NoError(err)
	s.Require().NotNil(l)

	// Acquire (promoted primitive) contends on one key.
	k := s.key("factory")
	tok, ok, err := l.Acquire(ctx, k)
	s.Require().NoError(err)
	s.Require().True(ok)

	_, ok2, err := l.Acquire(ctx, k)
	s.Require().NoError(err)
	s.False(ok2, "a second Acquire via the factory-built lock must lose")

	s.Require().NoError(l.Release(ctx, k, tok))

	// Hold (the Locker method) acquires + manages a distinct key over real Redis.
	heldCtx, release, held, err := l.Hold(ctx, s.key("factory-hold"))
	s.Require().NoError(err)
	s.Require().True(held, "Hold on a free key should acquire")
	s.Require().NoError(heldCtx.Err(), "held context is live while held")
	release()
}
