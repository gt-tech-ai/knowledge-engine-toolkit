package cache

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/client"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Decorate runs Get, Set and Delete of base through the client tier as the client named
// name, and counts each Get as a hit or a miss (cache_hits_total / cache_misses_total,
// labelled by cache). A cache is fail-open and never retried: an operation the tier
// refuses (an open breaker, a timeout) reads as a miss or a dropped write. When base
// can invalidate (interfaces.CacheInvalidator), so can the result, through the tier.
func Decorate(
	base interfaces.ByteCache,
	decs decorate.Decorations,
	name string,
	opts ...client.Option,
) (interfaces.ByteCache, error) {
	b, err := client.NewBase[cacheOps](decs, name, opts...)
	if err != nil {
		return nil, err
	}
	d := &decorated{base: base, Base: b}
	if m := decs.Observability().Metrics; m != nil {
		d.hits = m.Counter("cache_hits_total", "Total cache hits", "cache")
		d.misses = m.Counter("cache_misses_total", "Total cache misses", "cache")
	}
	if inv, ok := base.(interfaces.CacheInvalidator); ok {
		return &invalidating{decorated: d, inv: inv}, nil
	}
	return d, nil
}

// cacheOps is the decorated cache's contract: the cache and its invalidation surface.
type cacheOps interface {
	interfaces.ByteCache
	interfaces.CacheInvalidator
}

// decorated runs a ByteCache's operations through the client tier.
type decorated struct {
	// base is the wrapped cache.
	base interfaces.ByteCache

	// hits and misses count Get outcomes; nil without a metrics collaborator.
	hits, misses interfaces.Counter

	// Base runs each operation through the client tier.
	client.Base
}

var _ interfaces.ByteCache = (*decorated)(nil)

// Get returns the cached value, or a miss when the key is absent or the tier refused.
func (d *decorated) Get(ctx context.Context, key string) ([]byte, bool) {
	var hit bool
	v, err := d.ExecOnce(ctx, "get", func(c context.Context) ([]byte, error) {
		v, ok := d.base.Get(c, key)
		hit = ok
		return v, nil
	})
	hit = hit && err == nil
	d.count(hit)
	if !hit {
		return nil, false
	}
	return v, true
}

// Set stores value under key; a refused write is dropped (the cache is fail-open).
func (d *decorated) Set(
	ctx context.Context,
	key string,
	value []byte,
	ttl time.Duration,
) {
	_ = d.DoOnce(ctx, "set", func(c context.Context) error {
		d.base.Set(c, key, value, ttl)
		return nil
	})
}

// Delete removes key; a refused delete is dropped (the entry expires by its TTL).
func (d *decorated) Delete(ctx context.Context, key string) {
	_ = d.DoOnce(ctx, "delete", func(c context.Context) error {
		d.base.Delete(c, key)
		return nil
	})
}

// count records a Get outcome.
func (d *decorated) count(hit bool) {
	switch {
	case hit && d.hits != nil:
		d.hits.Inc(d.Name())
	case !hit && d.misses != nil:
		d.misses.Inc(d.Name())
	}
}

// invalidating is a decorated cache whose backend can also invalidate.
type invalidating struct {
	*decorated

	// inv is the backend's invalidation surface.
	inv interfaces.CacheInvalidator
}

var _ interfaces.CacheInvalidator = (*invalidating)(nil)

// InvalidatePrefix removes every key under prefix, through the tier.
func (i *invalidating) InvalidatePrefix(ctx context.Context, prefix string) error {
	return i.DoOnce(ctx, "invalidate_prefix", func(c context.Context) error {
		return i.inv.InvalidatePrefix(c, prefix)
	})
}

// InvalidateAll clears the cache, through the tier.
func (i *invalidating) InvalidateAll(ctx context.Context) error {
	return i.DoOnce(ctx, "invalidate_all", func(c context.Context) error {
		return i.inv.InvalidateAll(c)
	})
}
