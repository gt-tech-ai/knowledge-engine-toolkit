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
		inner = "(" + inner + ")"
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
	return "(" + strings.Join(subs, " AND ") + ")"
}

func (stringBackend) Or(
	subs []string,
) string {
	return "(" + strings.Join(subs, " OR ") + ")"
}
func (stringBackend) Empty() string { return "TRUE" }
func (stringBackend) Order(col string, desc bool) string {
	if desc {
		return col + " DESC"
	}
	return col + " ASC"
}

// OrderJoin renders a joined ORDER BY as a readable correlated-subquery string, one term
// per join column, so CompileOrder's joined branch is exercised backend-agnostically.
func (stringBackend) OrderJoin(join types.JoinTarget, desc bool) string {
	dir := "ASC NULLS LAST"
	if desc {
		dir = "DESC NULLS FIRST"
	}
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
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	return fmt.Sprintf("CASE(%s;%s) %s", col, strings.Join(values, ","), dir)
}

// OrderDerived renders a computed/correlated ORDER BY — a correlated aggregate or a
// timestamp-plus-interval — as a readable string, so CompileOrder's derived branch is
// exercised backend-agnostically (a nil-armed spec degrades to a no-op, mirroring the Ent
// backend).
func (stringBackend) OrderDerived(d types.DerivedSort, desc bool) string {
	dir := "ASC NULLS LAST"
	if desc {
		dir = "DESC NULLS FIRST"
	}
	switch {
	case d.Aggregate != nil:
		a := d.Aggregate
		arg := "*"
		if a.Func != "count" {
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
	if field == "name" {
		return "title"
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
		clause := types.FilterClause{Field: "name", Operator: types.OpLike, Value: "rep"}
		assert.Equal(
			t,
			"title like rep",
			listquery.Compile[string, string](b, resolveNameToTitle, clause),
		)
	})

	t.Run("and composite preserves member order", func(t *testing.T) {
		t.Parallel()
		f := types.CompositeFilter{And: []types.Filter{
			types.FilterClause{Field: "status", Operator: types.OpEq, Value: "indexed"},
			types.FilterClause{Field: "name", Operator: types.OpLike, Value: "rep"},
		}}
		assert.Equal(
			t,
			"(status eq indexed AND title like rep)",
			listquery.Compile[string, string](b, resolveNameToTitle, f),
		)
	})

	t.Run(
		"joined clause compiles to a correlated EXISTS (multi-column ORs)",
		func(t *testing.T) {
			t.Parallel()
			// A member "name" filter joins users and ORs first_name/last_name — one
			// EXISTS, one join. The clause carries the JoinTarget ResolveFilter would
			// stamp in production.
			clause := types.FilterClause{
				Field: "name", Operator: types.OpLike, Value: "smith",
				Join: &types.JoinTarget{
					Table: "users", LocalKey: "user_id", TargetKey: "id",
					Columns: []string{"first_name", "last_name"},
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
			f := types.CompositeFilter{And: []types.Filter{
				types.FilterClause{
					Field:    "name",
					Operator: types.OpLike,
					Value:    "smith",
					Join: &types.JoinTarget{
						Table:     "users",
						LocalKey:  "user_id",
						TargetKey: "id",
						Columns:   []string{"first_name"},
					},
				},
				types.FilterClause{
					Field:    "email",
					Operator: types.OpLike,
					Value:    "corp",
					Join: &types.JoinTarget{
						Table:     "users",
						LocalKey:  "user_id",
						TargetKey: "id",
						Columns:   []string{"email"},
					},
				},
			}}
			assert.Equal(
				t,
				"(EXISTS(users WHERE users.id=<base>.user_id AND users.first_name like smith) AND "+
					"EXISTS(users WHERE users.id=<base>.user_id AND users.email like corp))",
				listquery.Compile[string, string](b, resolveNameToTitle, f),
			)
		},
	)

	t.Run("ResolveFilter stamps the join target from the JoinFn", func(t *testing.T) {
		t.Parallel()
		// ResolveFilter carries a field's JoinTarget onto the resolved clause so the
		// runner (folding with an identity resolver) still compiles it to an EXISTS; a
		// same-table field gets no join.
		joinOf := func(field string) *types.JoinTarget {
			if field == "name" {
				return &types.JoinTarget{
					Table:     "users",
					LocalKey:  "user_id",
					TargetKey: "id",
					Columns:   []string{"first_name"},
				}
			}
			return nil
		}
		joined := listquery.ResolveFilter(resolveNameToTitle, joinOf,
			types.FilterClause{
				Field: "name", Operator: types.OpLike, Value: "smith",
			}).(types.FilterClause)
		assert.NotNil(t, joined.Join)
		assert.Equal(t, "users", joined.Join.Table)

		plain := listquery.ResolveFilter(resolveNameToTitle, joinOf,
			types.FilterClause{
				Field: "status", Operator: types.OpEq, Value: "indexed",
			}).(types.FilterClause)
		assert.Nil(t, plain.Join)
		assert.Equal(t, "status", plain.Field)
	})

	t.Run("nested and(eq, or(like, eq))", func(t *testing.T) {
		t.Parallel()
		f := types.CompositeFilter{And: []types.Filter{
			types.FilterClause{Field: "status", Operator: types.OpEq, Value: "indexed"},
			types.CompositeFilter{Or: []types.Filter{
				types.FilterClause{Field: "name", Operator: types.OpLike, Value: "rep"},
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
				[]types.OrderField{{Field: "name", Desc: false}},
				"",
			)
			assert.Equal(t, []string{"title ASC", "created_at DESC", "id DESC"}, got)
		},
	)

	t.Run("dedup when created_at is the primary sort", func(t *testing.T) {
		t.Parallel()
		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: "created_at", Desc: false}},
			"created_at",
		)
		assert.Equal(t, []string{"created_at ASC", "id DESC"}, got)
	})

	t.Run("dedup when id is the primary sort", func(t *testing.T) {
		t.Parallel()
		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: "id", Desc: false}},
			"",
		)
		assert.Equal(t, []string{"id ASC", "created_at DESC"}, got)
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
				[]types.OrderField{{Field: "role", Desc: false}},
				"joined_at",
			)
			assert.Equal(t, []string{"role ASC", "joined_at DESC", "id DESC"}, got)
		},
	)

	t.Run("dedup when the passed keyset column is the primary sort", func(t *testing.T) {
		t.Parallel()
		got := listquery.CompileOrder[string, string](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: "joined_at", Desc: false}},
			"joined_at",
		)
		assert.Equal(t, []string{"joined_at ASC", "id DESC"}, got)
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
						Table: "users", LocalKey: "user_id", TargetKey: "id",
						Columns: []string{"first_name", "last_name"},
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
				"joined_at",
			)
			assert.Equal(t, []string{
				"(SELECT users.first_name WHERE users.id=<base>.user_id) ASC NULLS LAST, " +
					"(SELECT users.last_name WHERE users.id=<base>.user_id) ASC NULLS LAST",
				"joined_at DESC",
				"id DESC",
			}, got)
		},
	)

	// A value-ordinal sort field resolves via ordinalOf to a CASE ORDER BY over its
	// column's values, placed before the same-table tiebreaker + id — proving the
	// compiler branches to OrderOrdinal for an ordinal field.
	t.Run(
		"ordinal sort field renders OrderOrdinal before the tiebreakers",
		func(t *testing.T) {
			t.Parallel()
			ordinalOf := func(field string) []string {
				if field == "role" {
					return []string{"admin", "write", "read"}
				}
				return nil
			}
			got := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				ordinalOf,
				nil,
				[]types.OrderField{{Field: "role", Desc: false}},
				"granted_at",
			)
			assert.Equal(t, []string{
				"CASE(role;admin,write,read) ASC",
				"granted_at DESC",
				"id DESC",
			}, got)
		},
	)

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
						Table: "documents", LocalKey: "id", TargetKey: "connector_id",
						Func: "count", TargetSoftDeletes: true,
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
				"created_at",
			)
			assert.Equal(t, []string{
				"(SELECT COUNT(*) FROM documents WHERE documents.connector_id=<base>.id " +
					"AND documents.deleted_at IS NULL) DESC NULLS FIRST",
				"created_at DESC",
				"id DESC",
			}, gotAgg)

			gotInterval := listquery.CompileOrder[string, string](
				b,
				resolveNameToTitle,
				nil,
				nil,
				derivedOf,
				[]types.OrderField{{Field: "next_sync_at", Desc: false}},
				"created_at",
			)
			assert.Equal(t, []string{
				"(last_sync_at + CASE(schedule;hourly=>3600s,daily=>86400s)) ASC NULLS LAST",
				"created_at DESC",
				"id DESC",
			}, gotInterval)
		},
	)
}
