package repository

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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

	cacheReads(&roles, rc)
	cacheWrites(&roles, rc)

	return roles
}

// cacheReads serves Get and Exists from the cache where it can.
func cacheReads[T, P any, ID comparable](
	roles *decorate.Roles[T, P, ID], rc *roleCache[T, ID],
) {
	if get := roles.Get; get != nil {
		roles.Get = func(ctx context.Context, id ID) (*T, error) { return rc.get(ctx, id, get) }
	}

	if exists := roles.Exists; exists != nil {
		roles.Exists = func(ctx context.Context, id ID) (bool, error) {
			return rc.exists(ctx, id, exists)
		}
	}
}

// cacheWrites primes the cache on Create and evicts on Update and Delete.
func cacheWrites[T, P any, ID comparable](
	roles *decorate.Roles[T, P, ID], rc *roleCache[T, ID],
) {
	if create := roles.Create; create != nil && rc.cache.IDOf != nil {
		roles.Create = func(ctx context.Context, entity *T) (*T, error) {
			return rc.create(ctx, entity, create)
		}
	}

	if update := roles.Update; update != nil {
		roles.Update = func(ctx context.Context, id ID, entity *T) (*T, error) {
			return rc.update(ctx, id, entity, update)
		}
	}

	if del := roles.Delete; del != nil {
		roles.Delete = func(ctx context.Context, id ID) error { return rc.remove(ctx, id, del) }
	}
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

// exists answers from the cache outside a transaction; a miss asks the next tier.
func (rc *roleCache[T, ID]) exists(
	ctx context.Context, id ID, next func(context.Context, ID) (bool, error),
) (bool, error) {
	if interfaces.InTx(ctx) {
		return next(ctx, id)
	}

	if _, ok := rc.read(ctx, CacheKey(rc.name, id)); ok {
		return true, nil
	}

	return next(ctx, id)
}

// create primes the cache with a created entity.
func (rc *roleCache[T, ID]) create(
	ctx context.Context, entity *T, next func(context.Context, *T) (*T, error),
) (*T, error) {
	result, err := next(ctx, entity)
	if err != nil {
		return nil, err
	}

	rc.prime(ctx, CacheKey(rc.name, rc.cache.IDOf(result)), result)

	return result, nil
}

// update evicts the entity once the update succeeds.
func (rc *roleCache[T, ID]) update(
	ctx context.Context, id ID, entity *T, next func(context.Context, ID, *T) (*T, error),
) (*T, error) {
	result, err := next(ctx, id, entity)
	if err == nil {
		rc.evict(ctx, CacheKey(rc.name, id))
	}

	return result, err
}

// remove evicts the entity once the delete succeeds.
func (rc *roleCache[T, ID]) remove(
	ctx context.Context, id ID, next func(context.Context, ID) error,
) error {
	err := next(ctx, id)
	if err == nil {
		rc.evict(ctx, CacheKey(rc.name, id))
	}

	return err
}

// read returns the cached entity under key, if a current one is there.
func (rc *roleCache[T, ID]) read(ctx context.Context, key string) (*T, bool) {
	data, hit := rc.cache.Store.Get(ctx, key)
	if !hit {
		return nil, false
	}

	val, ok, err := cache.Decode[T](data, rc.cache.Version)
	if err != nil || !ok {
		return nil, false
	}

	return &val, true
}

// write caches entity under key, best effort.
func (rc *roleCache[T, ID]) write(ctx context.Context, key string, entity *T) {
	encoded, err := cache.Encode(*entity, rc.cache.Version)
	if err == nil {
		rc.cache.Store.Set(ctx, key, encoded, rc.cache.TTL)
	}
}

// prime caches a created entity: now outside a transaction, after the commit inside one.
// It encodes now, so a caller that edits entity afterwards cannot change the entry.
func (rc *roleCache[T, ID]) prime(ctx context.Context, key string, entity *T) {
	encoded, err := cache.Encode(*entity, rc.cache.Version)
	if err != nil {
		return
	}

	interfaces.RegisterAfterCommit(ctx, func() {
		rc.cache.Store.Set(ctx, key, encoded, rc.cache.TTL)
	})
}

// evict drops key now and, inside a transaction, again after the commit.
func (rc *roleCache[T, ID]) evict(ctx context.Context, key string) {
	rc.cache.Store.Delete(ctx, key)

	if interfaces.InTx(ctx) {
		interfaces.RegisterAfterCommit(ctx, func() { rc.cache.Store.Delete(ctx, key) })
	}
}

// get reads the entity from the cache, or through get on a miss, returning a copy.
// Inside a transaction it reads the store and leaves the cache alone.
func (rc *roleCache[T, ID]) get(
	ctx context.Context,
	id ID,
	get func(context.Context, ID) (*T, error),
) (*T, error) {
	if interfaces.InTx(ctx) {
		return get(ctx, id)
	}

	key := CacheKey(rc.name, id)
	if val, ok := rc.read(ctx, key); ok {
		return val, nil
	}

	v, err, _ := rc.sf.Do(key, func() (any, error) {
		result, err := get(ctx, id)
		if err != nil {
			return nil, err
		}

		rc.write(ctx, key, result)

		return result, nil
	})
	if err != nil {
		return nil, err
	}

	cached, ok := v.(*T)
	if !ok {
		return nil, apperr.New(
			apperr.CodeInternal,
			"repository cache: loaded value has the wrong type",
		)
	}

	entity := *cached

	return &entity, nil
}
