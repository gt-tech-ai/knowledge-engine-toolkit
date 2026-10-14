package repository

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/decorate"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/store"
)

// Settings tunes the read-through entity cache DecoratedFromConfig puts under the
// repository; the repository's decoration layers come from the decoration config's
// repositories tier (decoration.repositories), not from here.
type Settings struct {
	// CacheTTL is how long a cached entity stays valid. Used only when Deps.Cache
	// is non-nil.
	CacheTTL time.Duration

	// CacheVersion namespaces cache keys so a schema change can invalidate them.
	CacheVersion int
}

// Deps carries the repository's runtime collaborators: the Decorations its layers are
// built from, and the optional entity cache. Caching is enabled only when Cache is
// non-nil; IDOf enables warm-on-create caching — nil caches lazily on Get.
type Deps[T any, ID comparable] struct {
	// Cache, when non-nil, enables the read-through caching decorator.
	Cache interfaces.ByteCache
	// IDOf derives the cache key from an entity, enabling warm-on-create caching;
	// nil leaves entities cached lazily on Get.
	IDOf func(*T) ID
	// Decorations builds the store's and the repository's layers from the stores and
	// repositories tiers of the decoration config, under the repository's name.
	decorate.Decorations
}

// DecoratedFromConfig builds the decorated repository over entities named name, each tier
// decorated as the decoration config resolves it for name: the store behind the store
// tier (stores/store.Tier), the read-through entity cache over it when deps.Cache is
// non-nil, and the repository tier (Tier) on top. A decoration config that fails to
// build is CodeInvalidInput.
func DecoratedFromConfig[T, P any, ID comparable](
	entities interfaces.Store[T, P, ID],
	name string,
	settings Settings,
	deps Deps[T, ID],
) (interfaces.Repository[T, P, ID], error) {
	storeLayers, err := deps.Deps(store.Tier(), name)
	if err != nil {
		return nil, err
	}
	layers, err := deps.Deps(Tier(), name)
	if err != nil {
		return nil, err
	}
	s := store.Decorate(entities, name, storeLayers)
	if deps.Cache != nil {
		s = Cached(s, name, Cache[T, ID]{
			Store:   deps.Cache,
			IDOf:    deps.IDOf,
			TTL:     settings.CacheTTL,
			Version: settings.CacheVersion,
		})
	}
	return NewBase(name, layers).CRUD(s), nil
}
