package unit_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// stringBackend is a QueryBackend[string, string] that renders the list-query IR to a
// readable SQL-ish string. It lets the shared fold (Compile / CompileOrder) be exercised
// with NO datastore dependency — the payoff of the algebra design: the tree-walk, boolean
// composition, column resolution, no-op handling, and tiebreaker logic are all
// backend-independent.
type stringBackend struct{}

func (stringBackend) Clause(col string, op types.FilterOperator, v any) string {
	return fmt.Sprintf("%s %s %v", col, op, v)
}

// ExistsClause renders a joined-column predicate as a readable correlated-EXISTS string,
// mirroring what the Ent backend compiles to SQL — so the fold's join branch is exercised
// without a DB.
func (stringBackend) ExistsClause(
	join types.JoinTarget,
	op types.FilterOperator,
	v any,
) string {
	preds := make([]string, len(join.Columns))
	for i, col := range join.Columns {
		preds[i] = fmt.Sprintf("%s.%s %s %v", join.Table, col, op, v)
	}

	inner := strings.Join(preds, " OR ")
	if len(preds) > 1 {
		inner = textOpen + inner + textClose
	}

	if join.TargetSoftDeletes {
		inner += fmt.Sprintf(" AND %s.deleted_at IS NULL", join.Table)
	}

	return fmt.Sprintf("EXISTS(%s WHERE %s.%s=<base>.%s AND %s)",
		join.Table, join.Table, join.TargetKey, join.LocalKey, inner)
}

func (stringBackend) And(
	subs []string,
) string {
	return textOpen + strings.Join(subs, " AND ") + textClose
}

// nullsOrder is the sort suffix of a direction, nulls last on ascending.
func nullsOrder(desc bool) string {
	return map[bool]string{false: "ASC NULLS LAST", true: "DESC NULLS FIRST"}[desc]
}

func (stringBackend) Or(
	subs []string,
) string {
	return textOpen + strings.Join(subs, " OR ") + textClose
}
func (stringBackend) Empty() string { return "TRUE" }
func (stringBackend) Order(col string, desc bool) string {
	return col + map[bool]string{false: " ASC", true: " DESC"}[desc]
}

// OrderJoin renders a joined ORDER BY as a readable correlated-subquery string, one term
// per join column, so CompileOrder's joined branch is exercised backend-agnostically.
func (stringBackend) OrderJoin(join types.JoinTarget, desc bool) string {
	dir := nullsOrder(desc)

	terms := make([]string, len(join.Columns))
	for i, col := range join.Columns {
		terms[i] = fmt.Sprintf("(SELECT %s.%s WHERE %s.%s=<base>.%s) %s",
			join.Table, col, join.Table, join.TargetKey, join.LocalKey, dir)
	}

	return strings.Join(terms, ", ")
}

// OrderOrdinal renders a value-ordinal ORDER BY as a readable CASE string, so
// CompileOrder's ordinal branch is exercised backend-agnostically.
func (stringBackend) OrderOrdinal(col string, values []string, desc bool) string {
	dir := map[bool]string{false: "ASC", true: "DESC"}[desc]

	return fmt.Sprintf("CASE(%s;%s) %s", col, strings.Join(values, ","), dir)
}

// OrderDerived renders a computed/correlated ORDER BY — a correlated aggregate or a
// timestamp-plus-interval — as a readable string, so CompileOrder's derived branch is
// exercised backend-agnostically (a nil-armed spec degrades to a no-op, mirroring the Ent
// backend).
func (stringBackend) OrderDerived(d types.DerivedSort, desc bool) string {
	return orderDerivedRest(d, nullsOrder(desc))
}

// orderDerivedRest continues OrderDerived with its remaining steps.
func orderDerivedRest(d types.DerivedSort, dir string) string {
	switch {
	case d.Aggregate != nil:
		a := d.Aggregate
		arg := "*"

		if a.Func != textCount {
			arg = a.Table + "." + a.Column
		}

		soft := ""
		if a.TargetSoftDeletes {
			soft = fmt.Sprintf(" AND %s.deleted_at IS NULL", a.Table)
		}

		return fmt.Sprintf(
			"(SELECT %s(%s) FROM %s WHERE %s.%s=<base>.%s%s) %s",
			strings.ToUpper(
				a.Func,
			),
			arg,
			a.Table,
			a.Table,
			a.TargetKey,
			a.LocalKey,
			soft,
			dir,
		)
	case d.Interval != nil:
		iv := d.Interval
		cases := make([]string, len(iv.Cases))

		for i, c := range iv.Cases {
			cases[i] = fmt.Sprintf("%s=>%ds", c.Value, c.Seconds)
		}

		return fmt.Sprintf("(%s + CASE(%s;%s)) %s",
			iv.BaseColumn, iv.DiscriminatorColumn, strings.Join(cases, ","), dir)
	default:
		return "TRUE"
	}
}

// resolveNameToTitle maps the Document DTO field "name" to its storage column "title"
// (the real gen/go/listquery mapping); every other field resolves to itself.
func resolveNameToTitle(field string) string {
	if field == textName {
		return textTitle
	}

	return field
}

// TestCompile_FoldViaStringAlgebra tests the shared Compile/CompileOrder fold
// backend-agnostically.
//
// Why this test is important:
//   - The fold is the one piece of the multi-target compiler every backend reuses; a bug
//     in the tree-walk, boolean composition, column resolution, no-op, or tiebreaker
//     logic would corrupt every datastore's query. Testing it through a trivial string
//     algebra proves that logic independently of Ent, exactly as the SDK's isomorphism
//     claims it can be.
//
// What it tests:
//   - A nil filter and an empty composite both fold to the backend's Empty() (no-op /
//     match-all).
//   - A leaf clause resolves its DTO field to the storage column ("name" -> "title") and
//     renders the operator+value.
//   - $and / $or composites (incl. nesting) fold to And()/Or() preserving member order.
//   - CompileOrder resolves columns, appends the resource keyset column DESC + id DESC
//     tiebreakers (created_at by default, or a passed keyset column such as joined_at),
//     and dedups either tiebreaker symmetrically when the caller already sorts by it.
func TestCompile_FoldViaStringAlgebra(t *testing.T) {
	t.Parallel()

	b := stringBackend{}

	t.Run("nil filter is a no-op", func(t *testing.T) {
		t.Parallel()
		assert.Equal(
			t,
			"TRUE",
			listquery.Compile[string, string](b, resolveNameToTitle, nil),
		)
	})

	t.Run("empty composite is a no-op", func(t *testing.T) {
		t.Parallel()
		assert.Equal(
			t,
			"TRUE",
			listquery.Compile[string, string](
				b,
				resolveNameToTitle,
				types.CompositeFilter{},
			),
		)
	})

	t.Run("leaf clause resolves column and renders op/value", func(t *testing.T) {
		t.Parallel()

		clause := types.FilterClause{
			Field:    textName,
			Operator: types.OpLike,
			Value:    textRep,
		}
		assert.Equal(
			t,
			"title like rep",
			listquery.Compile[string, string](b, resolveNameToTitle, clause),
		)
	})

	t.Run("and composite preserves member order", func(t *testing.T) {
		t.Parallel()

		f := types.CompositeFilter{And: []types.Filter{
			types.FilterClause{
				Field:    textStatus,
				Operator: types.OpEq,
				Value:    textIndexed,
			},
			types.FilterClause{Field: textName, Operator: types.OpLike, Value: textRep},
		}}
		assert.Equal(
			t,
			"(status eq indexed AND title like rep)",
			listquery.Compile[string, string](b, resolveNameToTitle, f),
		)
	})

	testCompileFoldViaStringAlgebraRest4(t, b)
}

// testCompileFoldViaStringAlgebraRest4 continues TestCompile_FoldViaStringAlgebra with
// its remaining steps.
func testCompileFoldViaStringAlgebraRest4(t *testing.T, b stringBackend) {
	t.Helper()

	t.Run(
		"joined clause compiles to a correlated EXISTS (multi-column ORs)",
		func(t *testing.T) {
			t.Parallel()

			// A member "name" filter joins users and ORs first_name/last_name — one
			// EXISTS, one join. The clause carries the JoinTarget ResolveFilter would
			// stamp in production.
			clause := types.FilterClause{
				Field: textName, Operator: types.OpLike, Value: textSmith,
				Join: &types.JoinTarget{
					Table: textUsers, LocalKey: textUserID, TargetKey: textID,
					Columns: []string{textFirstName, textLastName},
				},
			}
			assert.Equal(t,
				"EXISTS(users WHERE users.id=<base>.user_id AND "+
					"(users.first_name like smith OR users.last_name like smith))",
				listquery.Compile[string, string](b, resolveNameToTitle, clause))
		},
	)

	t.Run(
		"two joined clauses fold to two INDEPENDENT EXISTS (no dedup)",
		func(t *testing.T) {
			t.Parallel()

			// Filtering by member name AND email — both on the users join — yields two
			// independent EXISTS, never a duplicate JOIN (the payoff of the EXISTS
			// mechanism).
			f := testCompileFoldViaStringAlgebraTable()
			assert.Equal(
				t,
				"(EXISTS(users WHERE users.id=<base>.user_id AND users.first_name like smith) AND "+
					"EXISTS(users WHERE users.id=<base>.user_id AND users.email like corp))",
				listquery.Compile[string, string](b, resolveNameToTitle, f),
			)
		},
	)

	testCompileFoldViaStringAlgebraRest2(t, b)
}

// testCompileFoldViaStringAlgebraTable builds the table TestCompile_FoldViaStringAlgebra
// runs.
func testCompileFoldViaStringAlgebraTable() types.CompositeFilter {
	return types.CompositeFilter{And: []types.Filter{
		types.FilterClause{
			Field:    textName,
			Operator: types.OpLike,
			Value:    textSmith,
			Join: &types.JoinTarget{
				Table:     textUsers,
				LocalKey:  textUserID,
				TargetKey: textID,
				Columns:   []string{textFirstName},
			},
		},
		types.FilterClause{
			Field:    textEmail,
			Operator: types.OpLike,
			Value:    "corp",
			Join: &types.JoinTarget{
				Table:     textUsers,
				LocalKey:  textUserID,
				TargetKey: textID,
				Columns:   []string{textEmail},
			},
		},
	}}
}

// testCompileFoldViaStringAlgebraRest2 continues TestCompile_FoldViaStringAlgebra with
// its remaining steps.
func testCompileFoldViaStringAlgebraRest2(t *testing.T, b stringBackend) {
	t.Helper()

	t.Run("ResolveFilter stamps the join target from the JoinFn", func(t *testing.T) {
		t.Parallel()

		// ResolveFilter carries a field's JoinTarget onto the resolved clause so the
		// runner (folding with an identity resolver) still compiles it to an EXISTS; a
		// same-table field gets no join.
		joinOf := func(field string) *types.JoinTarget {
			if field == textName {
				return &types.JoinTarget{
					Table:     textUsers,
					LocalKey:  textUserID,
					TargetKey: textID,
					Columns:   []string{textFirstName},
				}
			}

			return nil
		}
		joined := asType[types.FilterClause](
			t,
			listquery.ResolveFilter(resolveNameToTitle, joinOf,
				types.FilterClause{
					Field: textName, Operator: types.OpLike, Value: textSmith,
				}),
		)
		assert.NotNil(t, joined.Join)
		assert.Equal(t, textUsers, joined.Join.Table)

		plain := asType[types.FilterClause](
			t,
			listquery.ResolveFilter(resolveNameToTitle, joinOf,
				types.FilterClause{
					Field: textStatus, Operator: types.OpEq, Value: textIndexed,
				}),
		)
		assert.Nil(t, plain.Join)
		assert.Equal(t, textStatus, plain.Field)
	})

	testCompileFoldViaStringAlgebraRest7(t, b)
}

// testCompileFoldViaStringAlgebraRest7 continues testCompileFoldViaStringAlgebraRest2
// with
// its remaining steps.
func testCompileFoldViaStringAlgebraRest7(t *testing.T, b stringBackend) {
	t.Helper()

	t.Run("nested and(eq, or(like, eq))", func(t *testing.T) {
		t.Parallel()

		f := types.CompositeFilter{And: []types.Filter{
			types.FilterClause{
				Field:    textStatus,
				Operator: types.OpEq,
				Value:    textIndexed,
			},
			types.CompositeFilter{Or: []types.Filter{
				types.FilterClause{
					Field:    textName,
					Operator: types.OpLike,
					Value:    textRep,
				},
				types.FilterClause{Field: "format", Operator: types.OpEq, Value: "pdf"},
			}},
		}}
		assert.Equal(t,
			"(status eq indexed AND (title like rep OR format eq pdf))",
			listquery.Compile[string, string](b, resolveNameToTitle, f))
	})

	t.Run(
		"order resolves column and appends default created_at tiebreakers",
		func(t *testing.T) {
			t.Parallel()

			got := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				nil,
				nil,
				[]types.OrderField{{Field: textName, Desc: false}},
				"",
			)
			assert.Equal(t, []string{"title ASC", textCreatedAtDesc, textIDDesc}, got)
		},
	)

	testCompileFoldViaStringAlgebraRest(t, b)
}

// testCompileFoldViaStringAlgebraRest continues TestCompile_FoldViaStringAlgebra with
// its remaining steps.
func testCompileFoldViaStringAlgebraRest(t *testing.T, b stringBackend) {
	t.Helper()

	t.Run("dedup when created_at is the primary sort", func(t *testing.T) {
		t.Parallel()

		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: textCreatedAt, Desc: false}},
			textCreatedAt,
		)
		assert.Equal(t, []string{"created_at ASC", textIDDesc}, got)
	})

	t.Run("dedup when id is the primary sort", func(t *testing.T) {
		t.Parallel()

		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: textID, Desc: false}},
			"",
		)
		assert.Equal(t, []string{"id ASC", textCreatedAtDesc}, got)
	})

	t.Run(
		"join-table keyset column overrides the created_at tiebreaker",
		func(t *testing.T) {
			t.Parallel()

			// A membership join table keyed on joined_at (not created_at) passes its
			// keyset column, so the compiled ORDER BY never references a non-existent
			// created_at column.
			got := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				nil,
				nil,
				[]types.OrderField{{Field: textRole, Desc: false}},
				textJoinedAt,
			)
			assert.Equal(t, []string{"role ASC", "joined_at DESC", textIDDesc}, got)
		},
	)

	testCompileFoldViaStringAlgebraRest6(t, b)
}

// testCompileFoldViaStringAlgebraRest6 continues testCompileFoldViaStringAlgebraRest
// with its remaining steps.
func testCompileFoldViaStringAlgebraRest6(t *testing.T, b stringBackend) {
	t.Helper()

	t.Run("dedup when the passed keyset column is the primary sort", func(t *testing.T) {
		t.Parallel()

		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: textJoinedAt, Desc: false}},
			textJoinedAt,
		)
		assert.Equal(t, []string{"joined_at ASC", textIDDesc}, got)
	})

	// A joined sort field resolves via joinOf to a correlated-subquery ORDER BY term (one
	// per join column), placed before the same-table tiebreaker + id — proving the
	// compiler branches to OrderJoin for a joined field and still appends the
	// deterministic tiebreakers.
	t.Run(
		"joined sort field renders OrderJoin before the tiebreakers",
		func(t *testing.T) {
			t.Parallel()

			joinOf := func(field string) *types.JoinTarget {
				if field == "member_name" {
					return &types.JoinTarget{
						Table: textUsers, LocalKey: textUserID, TargetKey: textID,
						Columns: []string{textFirstName, textLastName},
					}
				}

				return nil
			}
			got := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				joinOf,
				nil,
				nil,
				[]types.OrderField{{Field: "member_name", Desc: false}},
				textJoinedAt,
			)
			assert.Equal(t, []string{
				"(SELECT users.first_name WHERE users.id=<base>.user_id) ASC NULLS LAST, " +
					"(SELECT users.last_name WHERE users.id=<base>.user_id) ASC NULLS LAST",
				"joined_at DESC",
				textIDDesc,
			}, got)
		},
	)

	testCompileFoldViaStringAlgebraRest3(t, b)
}

// testCompileFoldViaStringAlgebraRest3 continues testCompileFoldViaStringAlgebraRest
// with its remaining steps.
func testCompileFoldViaStringAlgebraRest3(t *testing.T, b stringBackend) {
	t.Helper()

	// A value-ordinal sort field resolves via ordinalOf to a CASE ORDER BY over its
	// column's values, placed before the same-table tiebreaker + id — proving the
	// compiler branches to OrderOrdinal for an ordinal field.
	t.Run(
		"ordinal sort field renders OrderOrdinal before the tiebreakers",
		func(t *testing.T) {
			t.Parallel()

			ordinalOf := func(field string) []string {
				if field == textRole {
					return []string{textAdmin, "write", textRead}
				}

				return nil
			}
			got := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				ordinalOf,
				nil,
				[]types.OrderField{{Field: textRole, Desc: false}},
				"granted_at",
			)
			assert.Equal(t, []string{
				"CASE(role;admin,write,read) ASC",
				"granted_at DESC",
				textIDDesc,
			}, got)
		},
	)

	testCompileFoldViaStringAlgebraRest5(t, b)
}

// testCompileFoldViaStringAlgebraRest5 continues testCompileFoldViaStringAlgebraRest3
// with its remaining steps.
func testCompileFoldViaStringAlgebraRest5(t *testing.T, b stringBackend) {
	t.Helper()

	// A derived sort field resolves via derivedOf to a correlated-aggregate / interval
	// ORDER BY, placed before the same-table tiebreaker + id — proving the compiler
	// branches to OrderDerived for a computed count (Documents/Members) and a
	// timestamp-plus-interval (Next Sync).
	t.Run(
		"derived sort field renders OrderDerived before the tiebreakers",
		func(t *testing.T) {
			t.Parallel()

			derivedOf := func(field string) *types.DerivedSort {
				switch field {
				case "document_count":
					return &types.DerivedSort{Aggregate: &types.AggregateSpec{
						Table: textDocuments, LocalKey: textID, TargetKey: "connector_id",
						Func: textCount, TargetSoftDeletes: true,
					}}
				case "next_sync_at":
					return &types.DerivedSort{Interval: &types.IntervalSpec{
						BaseColumn:          "last_sync_at",
						DiscriminatorColumn: "schedule",
						Cases: []types.IntervalCase{
							{Value: "hourly", Seconds: 3600},
							{Value: "daily", Seconds: 86400},
						},
					}}
				}

				return nil
			}

			gotAgg := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				nil,
				derivedOf,
				[]types.OrderField{{Field: "document_count", Desc: true}},
				textCreatedAt,
			)
			assert.Equal(t, []string{
				"(SELECT COUNT(*) FROM documents WHERE documents.connector_id=<base>.id " +
					"AND documents.deleted_at IS NULL) DESC NULLS FIRST",
				textCreatedAtDesc,
				textIDDesc,
			}, gotAgg)

			gotInterval := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				nil,
				derivedOf,
				[]types.OrderField{{Field: "next_sync_at", Desc: false}},
				textCreatedAt,
			)
			assert.Equal(t, []string{
				"(last_sync_at + CASE(schedule;hourly=>3600s,daily=>86400s)) ASC NULLS LAST",
				textCreatedAtDesc,
				textIDDesc,
			}, gotInterval)
		},
	)
}
