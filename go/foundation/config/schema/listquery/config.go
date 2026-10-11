// Package listquery is the config surface for the list-query value-suggestion facet:
// the row threshold above which a facet-eligible field is served from the maintained
// distinct_values table instead of a live DISTINCT, and the interval at which the
// reconcile job recomputes refcounts. Pure data (mirrors infra/S3Config), read by the
// suggest routing (threshold) and the reconcile job (interval).
package listquery

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Config groups the list-query platform's tunable knobs under the "listquery" key.
type Config struct {
	// Facet tunes the value-suggestion facet (listquery.facet.*).
	Facet FacetConfig `mapstructure:"facet"`
}

// FacetConfig tunes the access-partitioned suggestion facet.
type FacetConfig struct {
	// ReconcileInterval is how often the reconcile job recomputes facet refcounts from
	// the fact table, correcting incremental drift (and the only path that removes a
	// rename-orphaned value). It bounds the facet's worst-case staleness window.
	ReconcileInterval time.Duration `mapstructure:"reconcile_interval"`

	// RowThreshold is the fact-row count, within one access scope, above which a
	// facet-eligible field is served from the distinct_values facet; at or below it the
	// suggest uses a live scoped DISTINCT. It trades facet-maintenance cost for read
	// speed on large scopes.
	RowThreshold int `mapstructure:"row_threshold"`
}

// DefaultConfig returns the facet defaults: a 100k-row threshold (below which live
// DISTINCT is cheap enough) and an hourly reconcile.
func DefaultConfig() Config {
	return Config{
		Facet: FacetConfig{
			RowThreshold:      100000,
			ReconcileInterval: time.Hour,
		},
	}
}

// Validate rejects a non-positive threshold or interval.
func (c Config) Validate() error {
	if c.Facet.RowThreshold < 1 {
		return apperr.InvalidInput("listquery.facet.row_threshold must be >= 1")
	}
	if c.Facet.ReconcileInterval <= 0 {
		return apperr.InvalidInput("listquery.facet.reconcile_interval must be > 0")
	}
	return nil
}
