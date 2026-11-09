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
		{textString, listquery.String(textName), listquery.FieldString},
		{textInt, listquery.Int(textCount), listquery.FieldInt},
		{"float", listquery.Float("score"), listquery.FieldFloat},
		{"bool", listquery.Bool(textActive2), listquery.FieldBool},
		{"time", listquery.Time(textCreatedAt), listquery.FieldTime},
		{textID, listquery.ID("owner_id"), listquery.FieldID},
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
		got := base.WithJoin(
			&types.JoinTarget{Table: "teams", Columns: []string{textName}},
		)
		require.NotNil(t, got.Join)
		require.Equal(t, "teams", got.Join.Table)
		require.Nil(t, base.Join, "the source field is not mutated")
	})

	t.Run("WithOrdinal sets the value-ordinal sort order as a copy", func(t *testing.T) {
		t.Parallel()

		base := listquery.String(textStatus)
		got := base.WithOrdinal([]string{textPending, textActive2, "done"})
		require.Equal(t, []string{textPending, textActive2, "done"}, got.Ordinal)
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
		listquery.String(textName),
		new(new(listquery.String("team_name")).WithColumn("t_name")).
			WithJoin(&types.JoinTarget{Table: "teams", Columns: []string{textName}}),
		new(listquery.String(textStatus)).WithOrdinal([]string{textA, textB}),
	)

	t.Run(
		"Lookup finds a registered field and misses an unknown one",
		func(t *testing.T) {
			t.Parallel()

			f, ok := m.Lookup(textName)
			require.True(t, ok)
			require.Equal(t, textName, f.Name)

			_, ok = m.Lookup(textNope)
			require.False(t, ok)
		},
	)

	t.Run(
		"Column returns the mapped column, falling back to the field name",
		func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "t_name", m.Column("team_name"))
			require.Equal(t, textName, m.Column(textName))
			// Unknown field: safe fallback to the name itself.
			require.Equal(t, textUnknown, m.Column(textUnknown))
		},
	)

	t.Run("Join returns the joined target only for a joined field", func(t *testing.T) {
		t.Parallel()

		jt := m.Join("team_name")
		require.NotNil(t, jt)
		require.Equal(t, "teams", jt.Table)
		require.Nil(t, m.Join(textName), "a same-table field has no join")
		require.Nil(t, m.Join(textUnknown), "an unknown field has no join")
	})

	t.Run(
		"Ordinal returns the value ordinal only for an ordinal field",
		func(t *testing.T) {
			t.Parallel()
			require.Equal(t, []string{textA, textB}, m.Ordinal(textStatus))
			require.Nil(t, m.Ordinal(textName), "a field with no ordinal returns nil")
			require.Nil(t, m.Ordinal(textUnknown), "an unknown field returns nil")
		},
	)
}
