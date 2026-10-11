package decorators

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
)

// cachingDecorator implements cache-aside (read-through) caching for
// single-entity Get operations. Uses the foundation cache envelope for
// JSON serialization with schema version checking.
//
// Cache-aside flow for Get:
//  1. Check ByteCache for key "repo:<name>:<id>"
//  2. On hit: decode envelope → version match? return cached T : treat as miss
//  3. On miss: call inner.Get → encode result → ByteCache.Set → return
//
// Mutations (Update, Delete) evict the cached entry after the inner operation
// succeeds. Create, List, and Exists pass through unchanged.
type cachingDecorator[T any, P any, ID comparable] struct {
	// inner is the next repository in the decorator chain.
	inner interfaces.DecoratedRepository[T, P, ID]

	// cache is the byte cache for read-through support.
	cache interfaces.ByteCache

	// sf collapses concurrent cache-miss loads for the same key into a single
	// inner.Get, so a cold key under a request stampede hits the backend once.
	sf singleflight.Group

	// idOf extracts an entity's id so Create can warm the cache with the freshly
	// created value. Nil disables warm-on-create (the entity is cached lazily on
	// its first Get instead).
	idOf func(*T) ID

	// name is the repository name used as a cache key prefix.
	name string

	// ttl is the per-entry TTL for cached values. Zero means use ByteCache default.
	ttl time.Duration

	// version is the schema version stamped on each envelope. Entries with
	// a different version are treated as misses.
	version int
}

// readCache returns a decoded, version-matching cached entity for key, or
// ok=false on a miss, wrong-version, or decode error (all treated as a miss).
func (d *cachingDecorator[T, P, ID]) readCache(
	ctx context.Context,
	key string,
) (*T, bool) {
	if data, hit := d.cache.Get(ctx, key); hit {
		if val, ok, err := cache.Decode[T](data, d.version); err == nil && ok {
			return &val, true
		}
	}
	return nil, false
}

// CacheKey builds the cache key for an entity in the named repository, namespaced
// so distinct repositories never collide on the same ID. It is the single source
// of truth for the key format, shared by the caching decorator and by
// repositories that must evict an entry for a write performed outside the
// decorator chain (e.g. a transaction-scoped status update that bypasses Update
// and so bypasses this decorator's own eviction).
func CacheKey(name string, id any) string {
	return fmt.Sprintf("repo:%s:%v", name, id)
}

// cacheKey builds the cache key for an entity in this decorator's repository.
func (d *cachingDecorator[T, P, ID]) cacheKey(id ID) string {
	return CacheKey(d.name, id)
}

// Get serves the entity from cache on a version-matching hit; otherwise it
// fetches from the inner repository and populates the cache before returning.
func (d *cachingDecorator[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	key := d.cacheKey(id)
	if val, ok := d.readCache(ctx, key); ok {
		return val, nil
	}
	// collapse concurrent misses on this key into a single inner.Get + fill,
	// so a request stampede on a cold key loads the backend once.
	v, err, _ := d.sf.Do(key, func() (any, error) {
		result, getErr := d.inner.Get(ctx, id)
		if getErr != nil {
			return nil, getErr
		}
		if encoded, encErr := cache.Encode(*result, d.version); encErr == nil {
			d.cache.Set(ctx, key, encoded, d.ttl)
		}
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	// Return an independent copy: singleflight shares one *T among concurrent
	// callers, whereas the non-collapsed path returned a distinct *T per caller.
	entity := *v.(*T)
	return &entity, nil
}

// List passes through to the inner repository; list results are not cached
// because their cache keys would depend on the full filter and page inputs.
func (d *cachingDecorator[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return d.inner.List(ctx, params, page)
}

// Create writes through to the inner repository and, on success, warms the cache
// with the freshly created entity so a subsequent Get is a hit. Warming is
// safe on create — there is no prior version to race with. When no id-extractor
// is configured the entity is instead cached lazily on its first Get.
func (d *cachingDecorator[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	result, err := d.inner.Create(ctx, entity)
	if err != nil {
		return nil, err
	}
	if d.idOf != nil {
		if encoded, encErr := cache.Encode(*result, d.version); encErr == nil {
			d.cache.Set(ctx, d.cacheKey(d.idOf(result)), encoded, d.ttl)
		}
	}
	return result, nil
}

// Update writes through to the inner repository and, on success, evicts the
// stale cache entry so the next Get reloads the fresh value.
func (d *cachingDecorator[T, P, ID]) Update(
	ctx context.Context,
	id ID,
	entity *T,
) (*T, error) {
	result, err := d.inner.Update(ctx, id, entity)
	if err == nil {
		d.cache.Delete(ctx, d.cacheKey(id))
	}
	return result, err
}

// Delete removes the entity via the inner repository and, on success, evicts
// its cache entry to avoid serving a deleted record.
func (d *cachingDecorator[T, P, ID]) Delete(ctx context.Context, id ID) error {
	err := d.inner.Delete(ctx, id)
	if err == nil {
		d.cache.Delete(ctx, d.cacheKey(id))
	}
	return err
}

// Exists reports whether the entity exists. A cache hit answers true without the
// backend — a cached entity definitively exists; a miss is inconclusive
// (the entity may exist but be uncached) and falls through to the inner repo.
func (d *cachingDecorator[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	if _, ok := d.readCache(ctx, d.cacheKey(id)); ok {
		return true, nil
	}
	return d.inner.Exists(ctx, id)
}
