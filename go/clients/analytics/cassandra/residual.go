package cassandra

import (
	"fmt"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TimeField is the filter field naming a fact's (grain-aligned) time; clauses on
// it also narrow the CQL clustering range.
const TimeField = "ts"

// stored is one stored row as the residual predicate sees it.
type stored struct {
	// ts is the row's grain-aligned time.
	ts time.Time
	// dims are the row's dimension values.
	dims map[string]string
}

// predicate decides whether a stored row matches the query's filter.
type predicate func(r stored) bool

// residual is the in-Go listquery backend: it compiles the whole filter into a
// predicate over stored rows. Dimensions live in a map column, which no CQL
// dialect both backends share can filter on, so every clause is evaluated here;
// clauses on TimeField are additionally pushed down as clustering bounds (a
// superset), so pushdown never changes an answer.
type residual struct{}

// Compile-time interface assertion.
var _ interfaces.QueryBackend[predicate, struct{}] = residual{}

// compileResidual folds f into a predicate (match-all for an empty filter).
func compileResidual(f types.Filter) predicate {
	return listquery.Compile[predicate, struct{}](
		residual{},
		func(field string) string { return field },
		f,
	)
}

// Clause matches one comparison against the row's time or dimension value.
func (residual) Clause(col string, op types.FilterOperator, v any) predicate {
	if col == TimeField {
		return func(r stored) bool { return compareTime(r.ts, op, v) }
	}
	return func(r stored) bool {
		got, ok := r.dims[col]
		return ok && compareString(got, op, v)
	}
}

// ExistsClause never matches: analytics rows have no joined tables.
func (residual) ExistsClause(types.JoinTarget, types.FilterOperator, any) predicate {
	return func(stored) bool { return false }
}

// And matches when every sub-predicate does.
func (residual) And(subs []predicate) predicate {
	return func(r stored) bool {
		for _, p := range subs {
			if !p(r) {
				return false
			}
		}
		return true
	}
}

// Or matches when any sub-predicate does.
func (residual) Or(subs []predicate) predicate {
	return func(r stored) bool {
		for _, p := range subs {
			if p(r) {
				return true
			}
		}
		return false
	}
}

// Empty matches every row.
func (residual) Empty() predicate { return func(stored) bool { return true } }

// Order is unused: rows are ordered by the reducer, not the store.
func (residual) Order(string, bool) struct{} { return struct{}{} }

// OrderJoin is unused.
func (residual) OrderJoin(types.JoinTarget, bool) struct{} { return struct{}{} }

// OrderOrdinal is unused.
func (residual) OrderOrdinal(string, []string, bool) struct{} { return struct{}{} }

// OrderDerived is unused.
func (residual) OrderDerived(types.DerivedSort, bool) struct{} { return struct{}{} }

// compareString applies op to a dimension value; operands are compared as text.
// OpLike is a case-insensitive substring match: unlike a SQL LIKE, `%` and `_`
// are literal characters here, not wildcards.
func compareString(got string, op types.FilterOperator, v any) bool {
	switch op {
	case types.OpEq:
		return got == fmt.Sprint(v)
	case types.OpNe:
		return got != fmt.Sprint(v)
	case types.OpIn, types.OpNotIn:
		found := false
		if list, ok := v.([]any); ok {
			for _, item := range list {
				found = found || got == fmt.Sprint(item)
			}
		}
		return found == (op == types.OpIn)
	case types.OpLike:
		return strings.Contains(strings.ToLower(got), strings.ToLower(fmt.Sprint(v)))
	case types.OpGt:
		return got > fmt.Sprint(v)
	case types.OpGte:
		return got >= fmt.Sprint(v)
	case types.OpLt:
		return got < fmt.Sprint(v)
	case types.OpLte:
		return got <= fmt.Sprint(v)
	default:
		return false
	}
}

// compareTime applies op to the row's time; a non-time operand never matches.
func compareTime(got time.Time, op types.FilterOperator, v any) bool {
	want, ok := v.(time.Time)
	if !ok {
		return false
	}
	switch op {
	case types.OpEq:
		return got.Equal(want)
	case types.OpNe:
		return !got.Equal(want)
	case types.OpGt:
		return got.After(want)
	case types.OpGte:
		return !got.Before(want)
	case types.OpLt:
		return got.Before(want)
	case types.OpLte:
		return !got.After(want)
	case types.OpIn, types.OpNotIn, types.OpLike:
		// A set or pattern operator has no meaning against one timestamp.
		return false
	default:
		return false
	}
}

// pushdown narrows [lo, hi) by the top-level AND clauses on TimeField. The
// bounds are widened to whole milliseconds (CQL timestamp precision) so the range
// is always a superset of the matching rows; the residual predicate is exact.
func pushdown(f types.Filter, lo, hi time.Time) (from, to time.Time) {
	var clauses []types.Filter
	switch v := f.(type) {
	case types.FilterClause:
		clauses = []types.Filter{v}
	case types.CompositeFilter:
		clauses = v.And
	}
	for _, c := range clauses {
		clause, ok := c.(types.FilterClause)
		if !ok || clause.Field != TimeField {
			continue
		}
		at, ok := clause.Value.(time.Time)
		if !ok {
			continue
		}
		switch clause.Operator {
		case types.OpGt, types.OpGte:
			if down := at.Truncate(time.Millisecond); down.After(lo) {
				lo = down
			}
		case types.OpLt, types.OpLte:
			if up := at.Truncate(time.Millisecond).Add(time.Millisecond); up.Before(hi) {
				hi = up
			}
		case types.OpEq:
			lo, hi = maxTime(
				lo,
				at.Truncate(time.Millisecond),
			), minTime(
				hi,
				at.Truncate(time.Millisecond).Add(time.Millisecond),
			)
		case types.OpNe, types.OpIn, types.OpNotIn, types.OpLike:
			// These do not bound the range; the residual predicate applies them.
		}
	}
	return lo, hi
}

// maxTime returns the later of a and b.
func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// minTime returns the earlier of a and b.
func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
