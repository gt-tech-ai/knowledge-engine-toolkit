package repository

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
)

// Cache configures the read-through entity cache Cached adds beneath a repository.
type Cache[T any, ID comparable] struct {
	// Store holds the encoded entities.
	Store interfaces.ByteCache

	// IDOf, when non-nil, keys a created entity so Create primes the cache.
	IDOf func(*T) ID

	// TTL bounds how long an entity stays cached.
	TTL time.Duration

	// Version tags each encoded entry, so a schema change invalidates old entries.
	Version int
}

// CacheKey is the cache key of the entity id in the repository named name.
func CacheKey(name string, id any) string {
	return fmt.Sprintf("repo:%s:%v", name, id)
}

// Cached returns store with a read-through entity cache: Get reads the cache first and
// fills it on a miss (concurrent misses for one id share one read), Exists answers a hit
// from the cache, Create primes it when c.IDOf is set, and Update and Delete evict the
// entity. List always reads the store. name keys the entries (CacheKey).
func Cached[T, P any, ID comparable](
	store interfaces.Store[T, P, ID],
	name string,
	c Cache[T, ID],
) interfaces.Store[T, P, ID] {
	return &cached[T, P, ID]{Store: store, cache: c, name: name}
}

// cached is the read-through cache over a store.
type cached[T, P any, ID comparable] struct {
	interfaces.Store[T, P, ID]

	// sf shares one store read among concurrent misses for an id.
	sf singleflight.Group

	// name keys the entries.
	name string

	// cache configures the entries.
	cache Cache[T, ID]
}

// read returns the cached entity under key, if a current one is there.
func (c *cached[T, P, ID]) read(ctx context.Context, key string) (*T, bool) {
	data, hit := c.cache.Store.Get(ctx, key)
	if !hit {
		return nil, false
	}
	val, ok, err := cache.Decode[T](data, c.cache.Version)
	if err != nil || !ok {
		return nil, false
	}
	return &val, true
}

// write caches entity under key, best effort.
func (c *cached[T, P, ID]) write(ctx context.Context, key string, entity *T) {
	if encoded, err := cache.Encode(*entity, c.cache.Version); err == nil {
		c.cache.Store.Set(ctx, key, encoded, c.cache.TTL)
	}
}

// Get reads the entity from the cache, or from the store on a miss.
func (c *cached[T, P, ID]) Get(ctx context.Context, id ID) (*T, error) {
	key := CacheKey(c.name, id)
	if val, ok := c.read(ctx, key); ok {
		return val, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		result, err := c.Store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		c.write(ctx, key, result)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	entity := *v.(*T)
	return &entity, nil
}

// List reads the store; pages are never cached.
func (c *cached[T, P, ID]) List(
	ctx context.Context,
	params P,
	page types.PageRequest,
) (*types.Page[T], error) {
	return c.Store.List(ctx, params, page)
}

// Create persists the entity and, when IDOf is set, primes the cache with it.
func (c *cached[T, P, ID]) Create(ctx context.Context, entity *T) (*T, error) {
	result, err := c.Store.Create(ctx, entity)
	if err != nil {
		return nil, err
	}
	if c.cache.IDOf != nil {
		c.write(ctx, CacheKey(c.name, c.cache.IDOf(result)), result)
	}
	return result, nil
}

// Update modifies the entity and evicts it.
func (c *cached[T, P, ID]) Update(ctx context.Context, id ID, entity *T) (*T, error) {
	result, err := c.Store.Update(ctx, id, entity)
	if err == nil {
		c.cache.Store.Delete(ctx, CacheKey(c.name, id))
	}
	return result, err
}

// Delete removes the entity and evicts it.
func (c *cached[T, P, ID]) Delete(ctx context.Context, id ID) error {
	err := c.Store.Delete(ctx, id)
	if err == nil {
		c.cache.Store.Delete(ctx, CacheKey(c.name, id))
	}
	return err
}

// Exists answers a cached entity without the store.
func (c *cached[T, P, ID]) Exists(ctx context.Context, id ID) (bool, error) {
	if _, ok := c.read(ctx, CacheKey(c.name, id)); ok {
		return true, nil
	}
	return c.Store.Exists(ctx, id)
}
