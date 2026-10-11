package unit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/listquery"
)

// TestListQueryFieldBuilders tests the allow-listed field constructors and their
// fluent copy-on-write modifiers.
//
// Why this test is important:
//   - The field builders declare the per-resource filter/sort allow-list — the
//     SQL-safety boundary. A wrong Type, a mutated shared Field, or a dropped
//     Column/Join/Ordinal would let an unintended column be filtered or sorted.
//
// What it tests:
//   - Each typed constructor sets Name, a defaulted Column, and the right Type; the
//     With* modifiers return a COPY carrying only the intended override.
func TestListQueryFieldBuilders(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		field listquery.Field
		typ   listquery.FieldType
	}{
		{"string", listquery.String("name"), listquery.FieldString},
		{"int", listquery.Int("count"), listquery.FieldInt},
		{"float", listquery.Float("score"), listquery.FieldFloat},
		{"bool", listquery.Bool("active"), listquery.FieldBool},
		{"time", listquery.Time("created_at"), listquery.FieldTime},
		{"id", listquery.ID("owner_id"), listquery.FieldID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.typ, tc.field.Type)
			// Column defaults to Name.
			require.Equal(t, tc.field.Name, tc.field.Column)
			require.NotEmpty(t, tc.field.Type.String())
		})
	}

	t.Run("WithColumn overrides only the storage column", func(t *testing.T) {
		t.Parallel()
		base := listquery.String("display_name")
		got := base.WithColumn("full_name")
		require.Equal(t, "full_name", got.Column)
		require.Equal(t, "display_name", got.Name)
		// The original is unchanged (copy-on-write).
		require.Equal(t, "display_name", base.Column)
	})

	t.Run("WithJoin stamps a correlated-join target as a copy", func(t *testing.T) {
		t.Parallel()
		base := listquery.String("team_name")
		got := base.WithJoin(types.JoinTarget{Table: "teams", Columns: []string{"name"}})
		require.NotNil(t, got.Join)
		require.Equal(t, "teams", got.Join.Table)
		require.Nil(t, base.Join, "the source field is not mutated")
	})

	t.Run("WithOrdinal sets the value-ordinal sort order as a copy", func(t *testing.T) {
		t.Parallel()
		base := listquery.String("status")
		got := base.WithOrdinal([]string{"pending", "active", "done"})
		require.Equal(t, []string{"pending", "active", "done"}, got.Ordinal)
		require.Nil(t, base.Ordinal, "the source field is not mutated")
	})
}

// TestListQueryMapAccessors tests the per-resource filter allow-list Map.
//
// Why this test is important:
//   - The Map is the runtime allow-list the parser/compiler consult; a lookup that
//     silently mis-resolves a column, join, or ordinal (or fails safe on an unknown
//     field) is the difference between a scoped query and an injection/leak.
//
// What it tests:
//   - Add registers fields by Name; Lookup/Column/Join/Ordinal return the registered
//     field's data for a known field and the documented safe fallback for an unknown one.
func TestListQueryMapAccessors(t *testing.T) {
	t.Parallel()

	m := listquery.NewMap().Add(
		listquery.String("name"),
		listquery.String("team_name").
			WithColumn("t_name").
			WithJoin(types.JoinTarget{Table: "teams", Columns: []string{"name"}}),
		listquery.String("status").WithOrdinal([]string{"a", "b"}),
	)

	t.Run(
		"Lookup finds a registered field and misses an unknown one",
		func(t *testing.T) {
			t.Parallel()
			f, ok := m.Lookup("name")
			require.True(t, ok)
			require.Equal(t, "name", f.Name)

			_, ok = m.Lookup("nope")
			require.False(t, ok)
		},
	)

	t.Run(
		"Column returns the mapped column, falling back to the field name",
		func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "t_name", m.Column("team_name"))
			require.Equal(t, "name", m.Column("name"))
			// Unknown field: safe fallback to the name itself.
			require.Equal(t, "unknown", m.Column("unknown"))
		},
	)

	t.Run("Join returns the joined target only for a joined field", func(t *testing.T) {
		t.Parallel()
		jt := m.Join("team_name")
		require.NotNil(t, jt)
		require.Equal(t, "teams", jt.Table)
		require.Nil(t, m.Join("name"), "a same-table field has no join")
		require.Nil(t, m.Join("unknown"), "an unknown field has no join")
	})

	t.Run(
		"Ordinal returns the value ordinal only for an ordinal field",
		func(t *testing.T) {
			t.Parallel()
			require.Equal(t, []string{"a", "b"}, m.Ordinal("status"))
			require.Nil(t, m.Ordinal("name"), "a field with no ordinal returns nil")
			require.Nil(t, m.Ordinal("unknown"), "an unknown field returns nil")
		},
	)
}
