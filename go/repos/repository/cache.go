package repository

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/cache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
)

// Cache configures the read-through entity cache an Entity puts beneath its roles.
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

// cached returns roles with a read-through entity cache: Get reads the cache first and
// fills it on a miss (concurrent misses for one id share one read), Exists answers a hit
// from the cache, Create primes it when c.IDOf is set, and Update and Delete evict the
// entity. List and the other roles always reach the store. name keys the entries
// (CacheKey).
//
// Inside a transaction (interfaces.InTx) the cache sees only committed rows: Get and
// Exists read the store, Create primes the cache after the commit
// (interfaces.RegisterAfterCommit), and Update and Delete evict at once and again after
// the commit, so a reader that raced the write cannot keep the pre-commit row.
func cached[T, P any, ID comparable](
	roles decorate.Roles[T, P, ID],
	c Cache[T, ID],
	name string,
) decorate.Roles[T, P, ID] {
	rc := &roleCache[T, ID]{cache: c, name: name}
	if get := roles.Get; get != nil {
		roles.Get = func(ctx context.Context, id ID) (*T, error) { return rc.get(ctx, id, get) }
	}
	if exists := roles.Exists; exists != nil {
		roles.Exists = func(ctx context.Context, id ID) (bool, error) {
			if interfaces.InTx(ctx) {
				return exists(ctx, id)
			}
			if _, ok := rc.read(ctx, CacheKey(name, id)); ok {
				return true, nil
			}
			return exists(ctx, id)
		}
	}
	if create := roles.Create; create != nil && c.IDOf != nil {
		roles.Create = func(ctx context.Context, entity *T) (*T, error) {
			result, err := create(ctx, entity)
			if err != nil {
				return nil, err
			}
			rc.prime(ctx, CacheKey(name, c.IDOf(result)), result)
			return result, nil
		}
	}
	if update := roles.Update; update != nil {
		roles.Update = func(ctx context.Context, id ID, entity *T) (*T, error) {
			result, err := update(ctx, id, entity)
			if err == nil {
				rc.evict(ctx, CacheKey(name, id))
			}
			return result, err
		}
	}
	if del := roles.Delete; del != nil {
		roles.Delete = func(ctx context.Context, id ID) error {
			err := del(ctx, id)
			if err == nil {
				rc.evict(ctx, CacheKey(name, id))
			}
			return err
		}
	}
	return roles
}

// roleCache holds the cache state a cached repository's roles share.
type roleCache[T any, ID comparable] struct {
	// sf shares one store read among concurrent misses for an id.
	sf singleflight.Group

	// name keys the entries.
	name string

	// cache configures the entries.
	cache Cache[T, ID]
}

// read returns the cached entity under key, if a current one is there.
func (c *roleCache[T, ID]) read(ctx context.Context, key string) (*T, bool) {
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
func (c *roleCache[T, ID]) write(ctx context.Context, key string, entity *T) {
	if encoded, err := cache.Encode(*entity, c.cache.Version); err == nil {
		c.cache.Store.Set(ctx, key, encoded, c.cache.TTL)
	}
}

// prime caches a created entity: now outside a transaction, after the commit inside one.
// It encodes now, so a caller that edits entity afterwards cannot change the entry.
func (c *roleCache[T, ID]) prime(ctx context.Context, key string, entity *T) {
	encoded, err := cache.Encode(*entity, c.cache.Version)
	if err != nil {
		return
	}
	interfaces.RegisterAfterCommit(ctx, func() {
		c.cache.Store.Set(ctx, key, encoded, c.cache.TTL)
	})
}

// evict drops key now and, inside a transaction, again after the commit.
func (c *roleCache[T, ID]) evict(ctx context.Context, key string) {
	c.cache.Store.Delete(ctx, key)
	if interfaces.InTx(ctx) {
		interfaces.RegisterAfterCommit(ctx, func() { c.cache.Store.Delete(ctx, key) })
	}
}

// get reads the entity from the cache, or through get on a miss, returning a copy.
// Inside a transaction it reads the store and leaves the cache alone.
func (c *roleCache[T, ID]) get(
	ctx context.Context,
	id ID,
	get func(context.Context, ID) (*T, error),
) (*T, error) {
	if interfaces.InTx(ctx) {
		return get(ctx, id)
	}
	key := CacheKey(c.name, id)
	if val, ok := c.read(ctx, key); ok {
		return val, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		result, err := get(ctx, id)
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
