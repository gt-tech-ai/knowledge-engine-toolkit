package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// TestFilterClause_IsEmpty tests the emptiness predicate on a basic filter clause.
//
// Why this test is important:
//   - Stores skip the WHERE clause when the filter is empty; a wrong IsEmpty would
//     either drop a real filter (too many rows returned) or emit an empty predicate.
//
// What it tests:
//   - A zero-value clause reports empty; a clause with a field + operator does not.
func TestFilterClause_IsEmpty(t *testing.T) {
	t.Parallel()

	assert.True(t, types.FilterClause{}.IsEmpty(), "zero-value clause is empty")
	assert.False(
		t,
		types.FilterClause{
			Field:    "name",
			Operator: types.OpEq,
			Value:    "report",
		}.IsEmpty(),
		"a populated clause is not empty",
	)
}

// TestCompositeFilter_IsEmpty tests the emptiness predicate on a composite filter,
// and (by holding a FilterClause in its And slice) that FilterClause satisfies the
// sealed Filter interface.
//
// Why this test is important:
//   - A composite with no sub-filters must read as empty so the store omits WHERE; a
//     populated one must apply. It also pins the Filter seam the parser + Ent compiler
//     depend on — if a clause type stopped satisfying Filter, this stops compiling.
//
// What it tests:
//   - An empty composite reports empty; one holding a sub-clause does not.
func TestCompositeFilter_IsEmpty(t *testing.T) {
	t.Parallel()

	assert.True(t, types.CompositeFilter{}.IsEmpty(), "no sub-filters → empty")
	assert.False(t,
		types.CompositeFilter{And: []types.Filter{
			types.FilterClause{Field: "name", Operator: types.OpEq, Value: "report"},
		}}.IsEmpty(),
		"a composite with a sub-clause is not empty")
}
