package unit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/cache/memory"
)

// TestCacheMemory_HoldsValuesForTheirTTLWithinItsBound tests the in-process cache
// backend.
//
// Why this test is important:
//   - A service with no Redis still needs to cache a value it reads often and that
//     rarely changes; the memory kind is that cache, so it must honor each TTL and stay
//     bounded however many keys pass through it.
//
// What it tests:
//   - The memory kind, built through the factory, returns a value just Set.
//   - A value expires at its TTL; a zero TTL uses the default; Delete removes a value.
//   - With a bound of 2, writing a third key evicts the least recently used one.
//   - Concurrent Sets and Gets are safe (run under -race).
func TestCacheMemory_HoldsValuesForTheirTTLWithinItsBound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("factory kind", func(t *testing.T) {
		t.Parallel()

		cfg := cache.DefaultConfig()

		cfg.Kind = cache.KindMemory

		c, err := cache.NewFromConfig(ctx, &cfg, cache.Deps{})
		require.NoError(t, err)
		c.Set(ctx, textK, []byte(textV), time.Minute)

		v, hit := c.Get(ctx, textK)
		assert.True(t, hit)
		assert.Equal(t, []byte(textV), v)
	})

	t.Run("ttl and delete", func(t *testing.T) {
		t.Parallel()

		now := time.Unix(1_700_000_000, 0)
		cacheValue := memory.New(10, time.Hour, func() time.Time { return now })
		cacheValue.Set(ctx, "short", []byte(textA), time.Minute)
		cacheValue.Set(ctx, "default", []byte(textB), 0)
		cacheValue.Set(ctx, textGone, []byte(textC), time.Hour)
		cacheValue.Delete(ctx, textGone)

		now = now.Add(time.Minute)

		_, hit := cacheValue.Get(ctx, "short")
		assert.False(t, hit, "a value expires at its ttl")

		_, hit = cacheValue.Get(ctx, "default")
		assert.True(t, hit, "a zero ttl uses the default")

		_, hit = cacheValue.Get(ctx, textGone)
		assert.False(t, hit, "a deleted value is gone")

		now = now.Add(time.Hour)
		_, hit = cacheValue.Get(ctx, "default")
		assert.False(t, hit, "the default ttl expires too")
	})

	testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundRest(ctx, t)
}

// testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundRest continues
// TestCacheMemory_HoldsValuesForTheirTTLWithinItsBound with its remaining steps.
func testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundRest(
	ctx context.Context, t *testing.T,
) {
	t.Helper()

	t.Run("bounded least recently used", func(t *testing.T) {
		t.Parallel()

		cacheValue := memory.New(2, time.Hour, time.Now)
		cacheValue.Set(ctx, textA, []byte("1"), 0)
		cacheValue.Set(ctx, textB, []byte("2"), 0)

		_, _ = cacheValue.Get(ctx, textA)
		cacheValue.Set(ctx, textC, []byte("3"), 0)

		_, hitA := cacheValue.Get(ctx, textA)
		_, hitB := cacheValue.Get(ctx, textB)
		_, hitC := cacheValue.Get(ctx, textC)

		assert.True(t, hitA)
		assert.False(t, hitB, "the least recently used key is evicted")
		assert.True(t, hitC)
	})

	t.Run("concurrent use", func(t *testing.T) {
		t.Parallel()

		testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundCase(ctx, t)
	})
}

// testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundCase is the body of a closure in
// testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundRest2.
func testCacheMemoryHoldsValuesForTheirTTLWithinItsBoundCase(
	ctx context.Context, t *testing.T,
) {
	t.Helper()

	cacheValue := memory.New(maxEntries64, time.Hour, time.Now)

	var group sync.WaitGroup

	for i := range iterations16 {
		group.Go(func() {
			for j := range 100 {
				key := fmt.Sprintf("k%d", (i*100+j)%128)
				cacheValue.Set(ctx, key, []byte(textV), 0)

				_, _ = cacheValue.Get(ctx, key)
			}
		})
	}

	group.Wait()
}
