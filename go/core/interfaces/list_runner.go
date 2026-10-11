package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ListRunner executes a resolved list query against one datastore entity. It is the
// EXECUTION seam of the list-query platform — the dual of QueryBackend (the
// query-LANGUAGE seam): QueryBackend folds the IR into predicates, ListRunner runs a
// composed query and returns typed rows.
//
// It is the single per-entity adapter the generic pagination template
// (foundation/listquery.Run) drives, so filter, sort, and keyset pagination are written
// ONCE in Run and every store's List is just "build the scoped base, hand it to Run." A
// runner is constructed per request over the request-scoped base query (tenant and access
// scoping already applied); it applies the spec's resolved predicates + order + window on
// top and executes. Ent has no generic query interface, so the adapter is per-entity —
// but it is dumb and uniform (a codegen candidate).
//
// T is the entity row type (e.g. *ent.Document).
type ListRunner[T any] interface {
	// List applies the resolved spec (predicates + order + window) to the scoped base
	// query and returns the matching rows in order.
	List(ctx context.Context, spec types.ListSpec) ([]T, error)

	// Count returns the number of rows matching the given resolved predicates over the
	// scoped base query (no window), for the offset pager's total_count — BOUNDED at
	// `bound` (≥ 1): it counts at most `bound` rows (a LIMIT-bounded id fetch, O(bound)),
	// so a per-page total_count never pays a full-table COUNT on a huge set. The caller
	// (foundation/listquery.Run) passes count_cap+1 and turns an over-cap tally into an
	// estimate; the runner just reports the bounded tally.
	Count(ctx context.Context, filters []types.Filter, bound int) (int, error)
}
