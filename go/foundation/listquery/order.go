package listquery

import (
	"strings"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Set is a per-resource allow-list of sortable field names — the SQL-safety boundary for
// sorting: a client can never order by a field outside the Set (an arbitrary/unindexed,
// or non-existent, column). A field not in the Set is dropped, never substituted; the
// deterministic default order comes from the compiler's keyset tiebreaker
// (foundation/listquery.CompileOrder), which is resource-appropriate (created_at,
// joined_at, …), not a fixed column the resource may not even have.
type Set struct {
	// fields is the allow-list of sortable field names.
	fields map[string]struct{}
}

// NewSet returns a sort allow-list of the given field names.
func NewSet(fields ...string) *Set {
	set := &Set{fields: make(map[string]struct{}, len(fields))}
	for _, f := range fields {
		set.fields[f] = struct{}{}
	}
	return set
}

// ParseList parses the scalar `sort` wire spec — a comma-separated list of `field[:dir]`
// tokens (e.g. "created_at:desc,name"; dir defaults to asc) — into an ordered
// []types.OrderField, dropping every non-allow-listed field. An empty spec, or one whose
// fields are all dropped, yields an empty result: the deterministic default order is then
// supplied by CompileOrder's keyset tiebreaker (resource-appropriate — created_at,
// joined_at, …), NOT a fixed column this resource may not have. It never errors and never
// string-concatenates client input, and it only ever returns allow-listed fields — the
// Set is the SQL-safety boundary for sorting. Wired at the transport edge.
func (s *Set) ParseList(spec string) []types.OrderField {
	var out []types.OrderField
	for tok := range strings.SplitSeq(spec, ",") {
		field, dir, _ := strings.Cut(strings.TrimSpace(tok), ":")
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, ok := s.fields[field]; !ok {
			continue // drop a non-allow-listed field rather than defaulting it per-token
		}
		out = append(out, types.OrderField{
			Field: field,
			Desc:  strings.EqualFold(strings.TrimSpace(dir), "desc"),
		})
	}
	// No fixed fallback field here: an empty out is intentional — CompileOrder appends
	// the resource's keyset column + id as the deterministic total order, so the ORDER BY
	// never references a column the resource lacks (e.g. created_at on a joined_at-keyed
	// join table).
	return out
}
