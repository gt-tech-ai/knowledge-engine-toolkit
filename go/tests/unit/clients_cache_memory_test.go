package unit_test

import (
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
		c, err := cache.NewFromConfig(ctx, cfg, cache.Deps{})
		require.NoError(t, err)
		c.Set(ctx, "k", []byte("v"), time.Minute)
		v, hit := c.Get(ctx, "k")
		assert.True(t, hit)
		assert.Equal(t, []byte("v"), v)
	})

	t.Run("ttl and delete", func(t *testing.T) {
		t.Parallel()
		now := time.Unix(1_700_000_000, 0)
		c := memory.New(10, time.Hour, func() time.Time { return now })
		c.Set(ctx, "short", []byte("a"), time.Minute)
		c.Set(ctx, "default", []byte("b"), 0)
		c.Set(ctx, "gone", []byte("c"), time.Hour)
		c.Delete(ctx, "gone")

		now = now.Add(time.Minute)
		_, hit := c.Get(ctx, "short")
		assert.False(t, hit, "a value expires at its ttl")
		_, hit = c.Get(ctx, "default")
		assert.True(t, hit, "a zero ttl uses the default")
		_, hit = c.Get(ctx, "gone")
		assert.False(t, hit, "a deleted value is gone")

		now = now.Add(time.Hour)
		_, hit = c.Get(ctx, "default")
		assert.False(t, hit, "the default ttl expires too")
	})

	t.Run("bounded least recently used", func(t *testing.T) {
		t.Parallel()
		c := memory.New(2, time.Hour, time.Now)
		c.Set(ctx, "a", []byte("1"), 0)
		c.Set(ctx, "b", []byte("2"), 0)
		_, _ = c.Get(ctx, "a")
		c.Set(ctx, "c", []byte("3"), 0)

		_, hitA := c.Get(ctx, "a")
		_, hitB := c.Get(ctx, "b")
		_, hitC := c.Get(ctx, "c")
		assert.True(t, hitA)
		assert.False(t, hitB, "the least recently used key is evicted")
		assert.True(t, hitC)
	})

	t.Run("concurrent use", func(t *testing.T) {
		t.Parallel()
		c := memory.New(64, time.Hour, time.Now)
		var wg sync.WaitGroup
		for i := range 16 {
			wg.Go(func() {
				for j := range 100 {
					key := fmt.Sprintf("k%d", (i*100+j)%128)
					c.Set(ctx, key, []byte("v"), 0)
					_, _ = c.Get(ctx, key)
				}
			})
		}
		wg.Wait()
	})
}
