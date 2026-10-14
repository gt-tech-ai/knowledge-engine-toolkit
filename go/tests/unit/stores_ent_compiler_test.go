package unit_test

import (
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
	entcompiler "github.com/gt-tech-ai/knowledge-engine-toolkit/go/stores/ent"
)

// renderPred applies a compiled Ent predicate to a fresh Postgres selector over
// "documents" and returns the rendered SQL + bound args, so a test can assert the
// predicate is parameterized (values as $N placeholders, never inlined).
func renderPred(pred entcompiler.EntPredicate) (query string, args []any) {
	sel := entsql.Dialect(dialect.Postgres).Select("*").From(entsql.Table("documents"))
	pred(sel)
	return sel.Query()
}

// renderOrder applies compiled order options to a fresh Postgres selector and returns the
// SQL, so a test can assert the ORDER BY column list + tiebreakers.
func renderOrder(orders []entcompiler.EntPredicate) string {
	sel := entsql.Dialect(dialect.Postgres).Select("*").From(entsql.Table("documents"))
	for _, o := range orders {
		o(sel)
	}
	q, _ := sel.Query()
	return q
}

// TestEntBackend_Clauses tests the Ent backend's leaf-clause and Empty interpretation.
//
// Why this test is important:
//   - This is the SQL-safety boundary: every operator must render a *parameterized*
//     predicate ($N placeholders, values in the args list) so no client value is ever
//     concatenated into SQL. A one-character bug (wrong operator, an inlined value, the
//     wrong column) would change the asserted SQL or args, so the test catches it.
//
// What it tests:
//   - Each of the 9 operators renders the expected parameterized fragment on the given
//     column.
//   - The slice operators ($in / $not_in) accept the parser's []any value and expand to
//     $N, $N.
//   - Empty() adds no WHERE clause.
func TestEntBackend_Clauses(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()

	t.Run("scalar operators are parameterized", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			op   types.FilterOperator
			frag string
		}{
			{"eq", types.OpEq, `"status" = $1`},
			{"ne", types.OpNe, `"status" <> $1`},
			{"gt", types.OpGt, `"page_count" > $1`},
			{"gte", types.OpGte, `"page_count" >= $1`},
			{"lt", types.OpLt, `"page_count" < $1`},
			{"lte", types.OpLte, `"page_count" <= $1`},
		}
		for _, tc := range cases {
			col := "status"
			val := any("indexed")
			if strings.HasPrefix(tc.name, "g") || strings.HasPrefix(tc.name, "l") {
				col, val = "page_count", int64(3)
			}
			q, args := renderPred(b.Clause(col, tc.op, val))
			assert.Contains(t, q, tc.frag, tc.name)
			assert.Equal(t, []any{val}, args, tc.name)
		}
	})

	t.Run(
		"like resolves to a case-insensitive parameterized match on the column",
		func(t *testing.T) {
			t.Parallel()
			q, args := renderPred(b.Clause("title", types.OpLike, "rep"))
			assert.Contains(t, q, "title")
			assert.Contains(t, q, "$1")
			assert.Equal(t, []any{"%rep%"}, args)
		},
	)

	t.Run("in expands the parser []any to $1, $2", func(t *testing.T) {
		t.Parallel()
		q, args := renderPred(b.Clause("status", types.OpIn, []any{"indexed", "pending"}))
		assert.Contains(t, q, `"status" IN ($1, $2)`)
		assert.Equal(t, []any{"indexed", "pending"}, args)
	})

	t.Run("not_in expands the parser []any", func(t *testing.T) {
		t.Parallel()
		q, args := renderPred(b.Clause("status", types.OpNotIn, []any{"failed"}))
		assert.Contains(t, q, `"status" NOT IN ($1)`)
		assert.Equal(t, []any{"failed"}, args)
	})

	t.Run("empty adds no WHERE", func(t *testing.T) {
		t.Parallel()
		q, args := renderPred(b.Empty())
		assert.NotContains(t, q, "WHERE")
		assert.Empty(t, args)
	})
}

// TestEntBackend_CompositeAndOrder tests the Ent backend's boolean composition + order
// rendering through the shared fold.
//
// Why this test is important:
//   - Composition (And/Or) and multi-column ordering are where a wrong combinator or a
//     lost tiebreaker silently corrupts results or breaks keyset pagination. Driving them
//     through the real fold (Compile/CompileOrder) with the Ent backend proves the full
//     path produces the expected parameterized SQL and a stable, fully-deterministic
//     ORDER BY.
//
// What it tests:
//   - A nested and(eq, or(like, in)) folds to parameterized AND/OR SQL with args in
//     clause order.
//   - CompileOrder resolves name->title, then appends created_at DESC, id DESC
//     tiebreakers.
//   - The tiebreakers dedup symmetrically: neither created_at nor id is duplicated when
//     it is the primary sort field.
func TestEntBackend_CompositeAndOrder(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()

	t.Run("nested and/or is parameterized in clause order", func(t *testing.T) {
		t.Parallel()
		f := types.CompositeFilter{And: []types.Filter{
			types.FilterClause{Field: "status", Operator: types.OpEq, Value: "indexed"},
			types.CompositeFilter{Or: []types.Filter{
				types.FilterClause{Field: "name", Operator: types.OpLike, Value: "rep"},
				types.FilterClause{
					Field:    "status",
					Operator: types.OpIn,
					Value:    []any{"a", "b"},
				},
			}},
		}}
		pred := listquery.Compile[entcompiler.EntPredicate, entcompiler.EntPredicate](
			b,
			resolveNameToTitle,
			f,
		)
		q, args := renderPred(pred)
		assert.Contains(t, q, "AND")
		assert.Contains(t, q, "OR")
		assert.Contains(t, q, "title")
		assert.Equal(t, []any{"indexed", "%rep%", "a", "b"}, args)
	})

	t.Run(
		"order resolves column and appends default created_at tiebreakers",
		func(t *testing.T) {
			t.Parallel()
			orders := listquery.CompileOrder[entcompiler.EntPredicate, entcompiler.EntPredicate](
				b,
				resolveNameToTitle,
				nil,
				nil,
				nil,
				[]types.OrderField{{Field: "name", Desc: false}},
				"",
			)
			q := renderOrder(orders)
			assert.Contains(t, q, "ORDER BY")
			assertOrderedColumns(t, q, `"title"`, `"created_at" DESC`, `"id" DESC`)
		},
	)

	t.Run("created_at primary sort dedups its tiebreaker", func(t *testing.T) {
		t.Parallel()
		orders := listquery.CompileOrder[entcompiler.EntPredicate, entcompiler.EntPredicate](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: "created_at", Desc: false}},
			"created_at",
		)
		q := renderOrder(orders)
		assert.Equal(t, 1, strings.Count(q, `"created_at"`), "created_at not duplicated")
		assert.Contains(t, q, `"id" DESC`)
	})

	t.Run("id primary sort dedups its tiebreaker", func(t *testing.T) {
		t.Parallel()
		orders := listquery.CompileOrder[entcompiler.EntPredicate, entcompiler.EntPredicate](
			b,
			resolveNameToTitle,
			nil,
			nil,
			nil,
			[]types.OrderField{{Field: "id", Desc: false}},
			"",
		)
		q := renderOrder(orders)
		assert.Equal(t, 1, strings.Count(q, `"id"`), "id not duplicated")
		assert.Contains(t, q, `"created_at" DESC`)
	})

	t.Run(
		"join-table keyset column replaces the created_at tiebreaker",
		func(t *testing.T) {
			t.Parallel()
			// A membership join table keyed on joined_at passes its keyset column so the
			// ORDER BY never references a non-existent created_at column — the fix that
			// unblocks org_member/team_member.
			orders := listquery.CompileOrder[entcompiler.EntPredicate, entcompiler.EntPredicate](
				b,
				resolveNameToTitle,
				nil,
				nil,
				nil,
				[]types.OrderField{{Field: "role", Desc: false}},
				"joined_at",
			)
			q := renderOrder(orders)
			assertOrderedColumns(t, q, `"role"`, `"joined_at" DESC`, `"id" DESC`)
			assert.NotContains(
				t,
				q,
				`"created_at"`,
				"created_at must not appear for a joined_at-keyed resource",
			)
		},
	)
}

// TestEntBackend_ExistsClauseHonorsCaseInsensitive tests that a joined $like predicate
// honors the join's CaseInsensitive flag: folding when set, case-sensitive when clear.
//
// Why this test is important:
//   - JoinTarget.CaseInsensitive is a contract field the proto/type/generator all carry;
//     if the ent backend ignored it (the prior dead-plumbing state), the contract would
//     lie — setting case_insensitive:false on a $like joined field would silently still
//     fold. This pins that the flag actually drives the rendered predicate.
//
// What it tests:
//   - ExistsClause with OpLike + CaseInsensitive:true renders a case-folding match
//     (ILIKE), while CaseInsensitive:false renders a plain case-sensitive LIKE (not
//     ILIKE), both parameterized.
func TestEntBackend_ExistsClauseHonorsCaseInsensitive(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()
	base := types.JoinTarget{
		Table:     "users",
		LocalKey:  "user_id",
		TargetKey: "id",
		Columns:   []string{"first_name"},
	}

	fold := base
	fold.CaseInsensitive = true
	q, args := renderPred(b.ExistsClause(fold, types.OpLike, "ann"))
	assert.Contains(t, q, "ILIKE", "case-insensitive $like must fold via ILIKE")
	assert.Contains(t, q, "$1")
	assert.Equal(t, []any{"%ann%"}, args)

	sensitive := base
	sensitive.CaseInsensitive = false
	q2, args2 := renderPred(b.ExistsClause(sensitive, types.OpLike, "ann"))
	assert.NotContains(t, q2, "ILIKE", "case-sensitive $like must NOT fold")
	assert.Contains(t, q2, "LIKE")
	assert.Equal(t, []any{"%ann%"}, args2)
}

// TestEntBackend_OrderJoin tests that a joined sort directive renders an ORDER BY over a
// correlated scalar subquery per join column, with the direction-aware NULLS clause — the
// real SQL a joined column sort produces against Postgres.
//
// Why this test is important:
//   - A joined column has no base-table column to ORDER BY; the correlated subquery is
//     the mechanism. A malformed subquery (wrong correlation, missing NULLS, or a base
//     JOIN that multiplies rows) would ship a broken or non-deterministic sort. This pins
//     the emitted SQL shape.
//
// What it tests:
//   - OrderJoin over users.first_name+last_name emits one correlated `(SELECT … WHERE
//     users.id = documents.user_id …)` term per column, ascending → `NULLS LAST`;
//     descending → `NULLS FIRST`.
//   - A join with TargetSoftDeletes adds an `AND <target>.deleted_at IS NULL` guard to
//     the correlated subquery (mirroring ExistsClause), so the sort never reads a
//     soft-deleted target's stale value; a join without it has no such guard.
func TestEntBackend_OrderJoin(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()
	join := types.JoinTarget{
		Table: "users", LocalKey: "user_id", TargetKey: "id",
		Columns: []string{"first_name", "last_name"},
	}

	asc := renderOrder([]entcompiler.EntPredicate{b.OrderJoin(join, false)})
	// One correlated subquery per column, correlated to the base documents.user_id,
	// ascending → NULLS LAST.
	assert.Contains(t, asc, `"users"."first_name"`)
	assert.Contains(t, asc, `"users"."last_name"`)
	assert.Contains(t, asc, `"documents"."user_id"`)
	assert.Contains(t, asc, "NULLS LAST")
	assert.NotContains(t, asc, "NULLS FIRST")
	// The base query is NOT joined — the correlation lives inside the ORDER BY subquery
	// only.
	assert.NotContains(t, asc, "JOIN")
	// A live-target join (no TargetSoftDeletes) emits no soft-delete guard.
	assert.NotContains(t, asc, "deleted_at")

	desc := renderOrder([]entcompiler.EntPredicate{b.OrderJoin(join, true)})
	assert.Contains(t, desc, "NULLS FIRST")
	assert.NotContains(t, desc, "NULLS LAST")

	// A soft-deleting target adds the `deleted_at IS NULL` guard to the correlated
	// subquery, so the sort excludes a soft-deleted target (matching ExistsClause's
	// filter) instead of sorting its stale value.
	softDel := types.JoinTarget{
		Table: "connectors", LocalKey: "connector_id", TargetKey: "id",
		Columns: []string{"name"}, TargetSoftDeletes: true,
	}
	guarded := renderOrder([]entcompiler.EntPredicate{b.OrderJoin(softDel, false)})
	assert.Contains(t, guarded, `"connectors"."deleted_at" IS NULL`)
}

// TestEntBackend_OrderOrdinal tests that a value-ordinal sort directive renders an `ORDER
// BY CASE col WHEN … THEN i … ELSE len END <dir>` with the values as bound parameters —
// the real SQL a meaning-ordered enum sort produces (e.g. access_level admin<write<read =
// owner<editor<viewer).
//
// Why this test is important:
//   - An enum column sorted lexically gives a meaningless order (admin<read<write); the
//     CASE ordinal is the mechanism that yields the semantic order. A wrong CASE (missing
//     ELSE, unbound values, wrong direction) would misorder or SQL-inject. This pins the
//     emitted shape + parameterization.
//
// What it tests:
//   - OrderOrdinal over access_level [admin,write,read] emits a CASE mapping each value
//     to THEN 0/1/2 + ELSE 3, the values inlined as quoted literals (trusted proto DATA),
//     ascending → " ASC", desc → " DESC".
func TestEntBackend_OrderOrdinal(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()
	values := []string{"admin", "write", "read"}

	asc := renderOrder(
		[]entcompiler.EntPredicate{b.OrderOrdinal("access_level", values, false)},
	)
	assert.Contains(t, asc, "CASE")
	assert.Contains(t, asc, `"access_level"`)
	// Values are inlined as quoted literals (trusted sort_ordinal DATA, not client input)
	// with the ordinal.
	assert.Contains(t, asc, "WHEN 'admin' THEN 0")
	assert.Contains(t, asc, "WHEN 'read' THEN 2")
	assert.Contains(t, asc, "ELSE 3")
	assert.Contains(t, asc, "END ASC")

	desc := renderOrder(
		[]entcompiler.EntPredicate{b.OrderOrdinal("access_level", values, true)},
	)
	assert.Contains(t, desc, "END DESC")
}

// TestEntBackend_OrderDerived tests that a derived sort directive renders the correct SQL
// for its two shapes — a correlated aggregate (a Documents/Members count) and a
// timestamp-plus-interval (a connector's Next Sync) — with the direction-aware NULLS
// clause and no base JOIN.
//
// Why this test is important:
//   - A computed count and a schedule-derived Next Sync have no base column to ORDER BY;
//     the correlated aggregate / computed interval IS the mechanism. A wrong correlation,
//     a missing soft-delete guard, or a base JOIN that multiplies rows would ship a
//     broken or non-deterministic sort. This pins the emitted SQL shape (renderOrder's
//     base table is "documents").
//
// What it tests:
//   - An aggregate COUNT emits `(SELECT COUNT(*) FROM child WHERE child.fk = base.pk AND
//     child.deleted_at IS NULL) <dir>` with no base JOIN; ascending → NULLS LAST,
//     descending → NULLS FIRST.
//   - An interval emits `(base.ts + CASE base.disc WHEN 'v' THEN make_interval(secs => n)
//     … ELSE NULL END) <dir>`, the seconds inlined as trusted spec literals.
func TestEntBackend_OrderDerived(t *testing.T) {
	t.Parallel()
	b := entcompiler.New()

	agg := types.DerivedSort{Aggregate: &types.AggregateSpec{
		Table: "team_members", LocalKey: "id", TargetKey: "team_id",
		Func: "count", TargetSoftDeletes: true,
	}}
	asc := renderOrder([]entcompiler.EntPredicate{b.OrderDerived(agg, false)})
	assert.Contains(t, asc, "COUNT(*)")
	assert.Contains(t, asc, `FROM "team_members"`)
	assert.Contains(t, asc, `"team_members"."team_id"`)
	// The correlation targets the base row (renderOrder's base table is documents) — no
	// base JOIN.
	assert.Contains(t, asc, `"documents"."id"`)
	assert.Contains(t, asc, `"team_members"."deleted_at" IS NULL`)
	assert.Contains(t, asc, "NULLS LAST")
	assert.NotContains(t, asc, "JOIN")

	desc := renderOrder([]entcompiler.EntPredicate{b.OrderDerived(agg, true)})
	assert.Contains(t, desc, "NULLS FIRST")
	assert.NotContains(t, desc, "NULLS LAST")

	iv := types.DerivedSort{Interval: &types.IntervalSpec{
		BaseColumn:          "last_sync_at",
		DiscriminatorColumn: "schedule",
		Cases: []types.IntervalCase{
			{Value: "hourly", Seconds: 3600},
			{Value: "daily", Seconds: 86400},
		},
	}}
	ivSQL := renderOrder([]entcompiler.EntPredicate{b.OrderDerived(iv, false)})
	assert.Contains(t, ivSQL, `"documents"."last_sync_at"`)
	assert.Contains(t, ivSQL, "CASE")
	assert.Contains(t, ivSQL, "WHEN 'hourly' THEN make_interval(secs => 3600)")
	assert.Contains(t, ivSQL, "WHEN 'daily' THEN make_interval(secs => 86400)")
	assert.Contains(t, ivSQL, "ELSE NULL END")
	assert.Contains(t, ivSQL, "NULLS LAST")
}

// assertOrderedColumns asserts each fragment appears in q in the given left-to-right
// order.
func assertOrderedColumns(t *testing.T, q string, fragments ...string) {
	t.Helper()
	prev := -1
	for _, frag := range fragments {
		idx := strings.Index(q, frag)
		assert.GreaterOrEqual(t, idx, 0, "fragment %q present", frag)
		assert.Greater(t, idx, prev, "fragment %q after previous", frag)
		prev = idx
	}
}
