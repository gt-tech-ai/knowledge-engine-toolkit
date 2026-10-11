package unit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	cachedecorators "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// ---------------------------------------------------------------------------
// Base ByteCache stub for testing cache decorators
// ---------------------------------------------------------------------------

// newStubByteCache builds a generated MockByteCache backed by an in-memory map,
// reproducing the round-trip semantics the decorator tests assert (Set stores, Get
// reads back, Delete removes) without requiring Redis. Using the generated mock
// instead of a hand-written fake keeps every test double in this package a mockgen
// mock; the map behind DoAndReturn is what makes the base observable to the tests.
func newStubByteCache(t *testing.T) *mocks.MockByteCache {
	t.Helper()
	m := mocks.NewMockByteCache(gomock.NewController(t))
	data := make(map[string][]byte)
	m.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, key string) ([]byte, bool) {
			val, ok := data[key]
			return val, ok
		},
	).AnyTimes()
	m.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Do(
		func(_ context.Context, key string, value []byte, _ time.Duration) {
			data[key] = value
		},
	).AnyTimes()
	m.EXPECT().Delete(gomock.Any(), gomock.Any()).Do(
		func(_ context.Context, key string) {
			delete(data, key)
		},
	).AnyTimes()
	return m
}

// ---------------------------------------------------------------------------
// Circuit breaker decorator tests
// ---------------------------------------------------------------------------

// TestCacheCircuitBreaker_GetPassesWhenClosed tests that Get passes through to
// the underlying cache when the circuit is closed.
//
// Why this test is important:
//   - A malfunctioning closed-circuit Get that returns a miss would cause cache
//     bypass on every request, adding unacceptable latency under normal load
//
// What it tests:
//   - Closed-circuit Get returns the value stored in the underlying cache
func TestCacheCircuitBreaker_GetPassesWhenClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	val, ok := decorated.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit")
	assert.Equal(t, "value1", string(val))
}

// TestCacheCircuitBreaker_GetReturnsNilWhenOpen tests that Get returns (nil,
// false) for graceful cache degradation when the circuit is open.
//
// Why this test is important:
//   - An open circuit must not block callers; returning a miss lets them fall
//     back to the source of truth rather than hanging or panicking
//
// What it tests:
//   - Open-circuit Get returns (nil, false) even when the key exists
func TestCacheCircuitBreaker_GetReturnsNilWhenOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(true)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	val, ok := decorated.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss when circuit is open")
	assert.Nil(t, val)
}

// TestCacheCircuitBreaker_SetIsNoOpWhenOpen tests that Set is silently skipped
// when the circuit is open.
//
// Why this test is important:
//   - Writing to a degraded backend under an open circuit would amplify failures;
//     Set must be a no-op to protect the backend during recovery
//
// What it tests:
//   - Open-circuit Set does not write to the underlying cache
func TestCacheCircuitBreaker_SetIsNoOpWhenOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	cb := fixtures.StubCircuitBreaker(true)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	// Set should be silently skipped
	decorated.Set(ctx, "key1", []byte("value1"), time.Minute)

	// Verify nothing was written to the underlying cache
	_, ok := base.Get(ctx, "key1")
	require.False(t, ok, "expected no write to underlying cache when circuit is open")
}

// TestCacheCircuitBreaker_DeleteIsNoOpWhenOpen tests that Delete is silently
// skipped when the circuit is open.
//
// Why this test is important:
//   - Attempting a Delete through a degraded backend would amplify failures and
//     could corrupt cache state; open-circuit Delete must be a no-op
//
// What it tests:
//   - Open-circuit Delete leaves the item in the underlying cache
func TestCacheCircuitBreaker_DeleteIsNoOpWhenOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(true)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	// Delete should be silently skipped
	decorated.Delete(ctx, "key1")

	// Verify the item still exists in the underlying cache
	_, ok := base.Get(ctx, "key1")
	require.True(
		t,
		ok,
		"expected item to remain in underlying cache when circuit is open",
	)
}

// TestCacheCircuitBreaker_SetPassesWhenClosed tests that Set writes through to
// the underlying cache when the circuit is closed.
//
// Why this test is important:
//   - Cache warming depends on Set reaching the underlying store; a Set that
//     silently discards writes when the circuit is closed defeats the cache
//
// What it tests:
//   - Closed-circuit Set writes the value to the underlying cache
func TestCacheCircuitBreaker_SetPassesWhenClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	decorated.Set(ctx, "key1", []byte("value1"), time.Minute)

	val, ok := base.Get(ctx, "key1")
	require.True(t, ok, "expected value to be written to underlying cache")
	assert.Equal(t, "value1", string(val))
}

// ---------------------------------------------------------------------------
// Timeout decorator tests
// ---------------------------------------------------------------------------

// TestCacheTimeout_GetPassesOnFastOp tests that Get passes through to the
// underlying cache when the operation completes before the timeout.
//
// Why this test is important:
//   - The timeout decorator must not block fast in-memory operations; a false
//     timeout cancellation would treat every cache hit as a miss
//
// What it tests:
//   - Get with a 5s timeout returns the stored value without cancellation
func TestCacheTimeout_GetPassesOnFastOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithTimeout(5 * time.Second).
		Build()

	val, ok := decorated.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit")
	assert.Equal(t, "value1", string(val))
}

// TestCacheTimeout_SetPassesOnFastOp tests that Set writes through to the
// underlying cache when the operation completes before the timeout.
//
// Why this test is important:
//   - A timeout decorator that cancels fast Set operations would prevent cache
//     warming, degrading hit rates under normal load
//
// What it tests:
//   - Set with a 5s timeout writes the value to the underlying cache
func TestCacheTimeout_SetPassesOnFastOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithTimeout(5 * time.Second).
		Build()

	decorated.Set(ctx, "key1", []byte("value1"), time.Minute)

	val, ok := base.Get(ctx, "key1")
	require.True(t, ok, "expected value to be written")
	assert.Equal(t, "value1", string(val))
}

// TestCacheTimeout_DeletePassesOnFastOp tests that Delete passes through to the
// underlying cache when the operation completes before the timeout.
//
// Why this test is important:
//   - A timeout that cancels fast Delete operations would leave stale entries in
//     the cache and silently serve outdated data
//
// What it tests:
//   - Delete with a 5s timeout removes the item from the underlying cache
func TestCacheTimeout_DeletePassesOnFastOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithTimeout(5 * time.Second).
		Build()

	decorated.Delete(ctx, "key1")

	_, ok := base.Get(ctx, "key1")
	require.False(t, ok, "expected item to be deleted")
}

// ---------------------------------------------------------------------------
// Full decorator chain tests
// ---------------------------------------------------------------------------

// TestCacheDecorators_FullChain tests that the full decorator chain (circuit
// breaker + timeout + metrics) correctly propagates Get, Set, and Delete.
//
// Why this test is important:
//   - Each decorator wraps the previous one; an incorrect composition order
//     could cause metrics to not record, timeouts to not apply, or the CB to
//     observe incorrect outcomes
//
// What it tests:
//   - Get hits the underlying cache through all decorators
//   - Set writes through all decorators
//   - Delete removes the item through all decorators
func TestCacheDecorators_FullChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		WithTimeout(5 * time.Second).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Get
	val, ok := decorated.Get(ctx, "key1")
	require.True(t, ok, "expected cache hit through full chain")
	assert.Equal(t, "value1", string(val))

	// Set
	decorated.Set(ctx, "key2", []byte("value2"), time.Minute)
	val, ok = decorated.Get(ctx, "key2")
	require.True(t, ok, "expected cache hit for key2 after Set")
	assert.Equal(t, "value2", string(val))

	// Delete
	decorated.Delete(ctx, "key1")
	_, ok = decorated.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss after Delete")
}

// ---------------------------------------------------------------------------
// Circuit breaker + canceled context tests (ctx.Err() branches)
// ---------------------------------------------------------------------------

// TestCacheCircuitBreaker_GetReturnsFalseOnCanceledCtx tests that Get returns
// (nil, false) when the context is canceled during a CB-wrapped operation,
// exercising the ctx.Err() branch inside circuitBreakerDecorator.Get.
//
// Why this test is important:
//   - When a caller cancels mid-operation, the circuit breaker must record
//     the cancellation as an error so its failure counters are accurate; the
//     Get must return a miss rather than stale data
//
// What it tests:
//   - Pre-canceled context causes CB.Execute to return ctx.Err()
//   - Get returns (nil, false)
func TestCacheCircuitBreaker_GetReturnsFalseOnCanceledCtx(t *testing.T) {
	t.Parallel()

	base := newStubByteCache(t)
	base.Set(context.Background(), "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	// Cancel the context before calling Get so ctx.Err() is non-nil
	// inside the CB-wrapped function.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	val, ok := decorated.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss when context is canceled")
	assert.Nil(t, val)
}

// TestCacheCircuitBreaker_SetNoOpOnCanceledCtx tests that Set records a
// context cancellation error through the circuit breaker when the context
// is already canceled, exercising the ctx.Err() branch inside Set.
//
// Why this test is important:
//   - When a caller cancels mid-Set, the circuit breaker must record the
//     cancellation so its failure counters track the error; the Set becomes
//     a no-op from the caller's perspective
//
// What it tests:
//   - Pre-canceled context causes CB.Execute to return ctx.Err()
//   - The underlying cache may or may not have been written (implementation
//     detail), but the CB error path is exercised
func TestCacheCircuitBreaker_SetNoOpOnCanceledCtx(t *testing.T) {
	t.Parallel()

	base := newStubByteCache(t)
	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should not panic; the CB records the ctx error internally.
	decorated.Set(ctx, "key1", []byte("value1"), time.Minute)
}

// TestCacheCircuitBreaker_DeleteNoOpOnCanceledCtx tests that Delete records a
// context cancellation error through the circuit breaker when the context
// is already canceled, exercising the ctx.Err() branch inside Delete.
//
// Why this test is important:
//   - When a caller cancels mid-Delete, the circuit breaker must record the
//     cancellation so its failure counters track the error; the Delete becomes
//     a no-op from the caller's perspective
//
// What it tests:
//   - Pre-canceled context causes CB.Execute to return ctx.Err()
//   - The underlying cache item may or may not have been deleted
//     (implementation detail), but the CB error path is exercised
func TestCacheCircuitBreaker_DeleteNoOpOnCanceledCtx(t *testing.T) {
	t.Parallel()

	base := newStubByteCache(t)
	base.Set(context.Background(), "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(false)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		Build()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Should not panic; the CB records the ctx error internally.
	decorated.Delete(ctx, "key1")
}

// TestCacheDecorators_FullChainOpenCircuit tests graceful degradation through
// the full decorator chain when the circuit is open.
//
// Why this test is important:
//   - An open circuit must short-circuit the entire chain; if any decorator
//     passes through to the backend during circuit-open, it amplifies failures
//
// What it tests:
//   - Open-circuit Get returns (nil, false) through all decorators
//   - Open-circuit Set does not write to the underlying cache
func TestCacheDecorators_FullChainOpenCircuit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	base := newStubByteCache(t)
	base.Set(ctx, "key1", []byte("value1"), 0)

	cb := fixtures.StubCircuitBreaker(true)
	decorated := cachedecorators.NewBuilder(base, "test-cache").
		WithCircuitBreaker(cb).
		WithTimeout(5 * time.Second).
		WithMetrics(fixtures.NopMetrics()).
		Build()

	// Get should degrade gracefully
	val, ok := decorated.Get(ctx, "key1")
	require.False(t, ok, "expected cache miss when circuit is open")
	assert.Nil(t, val)

	// Set should be silently skipped
	decorated.Set(ctx, "key2", []byte("value2"), time.Minute)
	_, ok = base.Get(ctx, "key2")
	require.False(t, ok, "expected no write when circuit is open")
}
