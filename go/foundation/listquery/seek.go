package listquery

import "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"

// SeekFilter builds the forward keyset SEEK predicate as a plain types.Filter: the
// lexicographic OR-expansion selecting exactly the rows strictly after the cursor row in
// the query's total order. Because it is an ordinary Filter, it needs no ORM-specific
// compiler — a store folds it with the SAME QueryBackend it uses for the user filter, and
// the seek can never drift from the ORDER BY (both derive from the same KeysetColumns
// sequence).
//
// cols is the fully-deterministic keyset column order (caller sort fields → tiebreaker →
// id, resolved, deduped); values[i] is the cursor's value for cols[i], already restored
// to its bound type (time.Time for a timestamp column, the id's native type for the id
// column). len(values) must equal len(cols).
//
// The expansion for columns c0,c1,…,cn (with per-column strict operator `>` for ASC / `<`
// for DESC):
//
//	strict(c0)  OR  ( eq(c0) AND ( strict(c1) OR ( eq(c1) AND … strict(cn) ) ) )
//
// Sortable columns are NOT NULL, so every term is a plain `>`/`<`/`=` comparison — no
// NULL-boundary handling. A nullable sortable column would require extending this with IS
// NULL terms.
func SeekFilter(cols []KeysetColumn, values []any) types.Filter {
	if len(cols) == 0 {
		return types.CompositeFilter{}
	}
	return seekFrom(cols, values, 0)
}

// seekFrom builds the OR-expansion for the suffix of the ordered columns starting at
// index i: `strict(col_i) OR (eq(col_i) AND seekFrom(i+1))`. The last column contributes
// only its strict term.
func seekFrom(cols []KeysetColumn, values []any, i int) types.Filter {
	strict := strictClause(cols[i], values[i])
	if i == len(cols)-1 {
		return strict
	}
	eq := types.FilterClause{
		Field:    cols[i].Column,
		Operator: types.OpEq,
		Value:    values[i],
	}
	tail := types.CompositeFilter{And: []types.Filter{eq, seekFrom(cols, values, i+1)}}
	return types.CompositeFilter{Or: []types.Filter{strict, tail}}
}

// strictClause builds the "strictly after the cursor at this column" comparison: `col >
// v` for an ascending column, `col < v` for a descending one. The Field is the
// already-resolved storage column, so a store compiles the seek with an identity
// resolver.
func strictClause(c KeysetColumn, v any) types.FilterClause {
	op := types.OpGt
	if c.Desc {
		op = types.OpLt
	}
	return types.FilterClause{Field: c.Column, Operator: op, Value: v}
}
