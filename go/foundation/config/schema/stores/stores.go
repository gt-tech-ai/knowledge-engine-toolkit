// Package stores is the config surface for the data (stores) layer: the page-size
// cap, the per-resource default page sizes and the list-query caches, so a deployer
// can tune pagination without a rebuild. Pure data (mirrors infra/S3Config),
// converted to the store call args by the consumer's provider.
package stores

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Cache tunes the list-query read-through caches: the
// value-suggestion cache and the total_count cache. Both are access-fingerprint
// keyed, so an authz change invalidates by key rotation. Disabling degrades to
// live queries (never incorrectness).
type Cache struct {
	// Enabled turns the suggest + count read-through cache decorators on. When
	// false the stores are wired straight through (no cache).
	Enabled bool `mapstructure:"enabled"`

	// SuggestTTL is the per-entry TTL for the value-suggestion cache. It also
	// bounds the RTBF exposure window (a purged value ages out within this TTL,
	// as there is no reach-in invalidation). 0 uses the ByteCache default.
	SuggestTTL time.Duration `mapstructure:"suggest_ttl"`

	// CountTTL is the per-entry TTL for the total_count cache (a coarse
	// generation bump on a resource write invalidates sooner). 0 uses the
	// ByteCache default.
	CountTTL time.Duration `mapstructure:"count_ttl"`
}

// Config tunes the data-layer pagination knobs.
type Config struct {
	// MaxPageSize caps every paginated LIMIT (clamps the page size in listquery.Run); 0 =
	// uncapped. The cap that closes the unbounded-LIMIT gap on client-supplied page
	// sizes.
	MaxPageSize int `mapstructure:"max_page_size"`

	// DefaultPageSize is the fallback page size when a request omits one.
	DefaultPageSize int `mapstructure:"default_page_size"`

	// CountCap bounds the numeric pager's total_count: the offset arm counts
	// matching rows only up to this cap, so a per-page total_count never pays a full-table
	// COUNT on a huge, unfiltered set. A result at or under the cap is exact; over it, the
	// pager shows the cap with total_is_estimate=true (rendered "N+"). 0 falls back to the
	// foundation default (listquery.DefaultCountCap), so a count is always bounded.
	CountCap int `mapstructure:"count_cap"`

	// Cache tunes the list-query read-through caches (suggest + count).
	Cache Cache `mapstructure:"cache"`

	// SlowQueryThreshold is the per-query duration above which the Ent guardrail
	// interceptor logs a warning (with request + tenant ids, never the args) so slow
	// reads are observable. 0 disables slow-query logging.
	SlowQueryThreshold time.Duration `mapstructure:"slow_query_threshold"`

	// ListHardCap is the safety-net row bound the Ent guardrail interceptor applies to
	// an otherwise-unbounded list (a query with no LIMIT set) — keyset pagination
	// is the real bound; this only catches a missing one. 0
	// disables the cap.
	ListHardCap int `mapstructure:"list_hard_cap"`
}

// DefaultConfig returns the store defaults. MaxPageSize defaults to 1000 — a cap
// that bounds a client-supplied LIMIT without affecting any normal request.
func DefaultConfig() Config {
	return Config{
		MaxPageSize:     1000,
		DefaultPageSize: 20,
		CountCap:        10000,
		Cache: Cache{
			Enabled:    true,
			SuggestTTL: 45 * time.Second,
			CountTTL:   60 * time.Second,
		},
		SlowQueryThreshold: 200 * time.Millisecond,
		ListHardCap:        10000,
	}
}

// Validate rejects non-positive page sizes and a max below the defaults.
func (c Config) Validate() error {
	if c.DefaultPageSize < 1 {
		return apperr.InvalidInput("stores.default_page_size must be >= 1")
	}
	if c.MaxPageSize < 0 {
		return apperr.InvalidInput("stores.max_page_size must be >= 0")
	}
	if c.MaxPageSize > 0 && c.MaxPageSize < c.DefaultPageSize {
		return apperr.InvalidInput("stores.max_page_size must be >= default_page_size")
	}
	if c.CountCap < 0 {
		return apperr.InvalidInput("stores.count_cap must be >= 0")
	}
	if c.Cache.SuggestTTL < 0 {
		return apperr.InvalidInput("stores.cache.suggest_ttl must be >= 0")
	}
	if c.Cache.CountTTL < 0 {
		return apperr.InvalidInput("stores.cache.count_ttl must be >= 0")
	}
	if c.SlowQueryThreshold < 0 {
		return apperr.InvalidInput("stores.slow_query_threshold must be >= 0")
	}
	if c.ListHardCap < 0 {
		return apperr.InvalidInput("stores.list_hard_cap must be >= 0")
	}
	return nil
}
