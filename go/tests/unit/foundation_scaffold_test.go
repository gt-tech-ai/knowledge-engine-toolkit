package unit_test

import (
	"maps"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/scaffold"
)

// scaffoldTemplates is a three-file template set, one file per policy.
func scaffoldTemplates() fstest.MapFS {
	return fstest.MapFS{
		"{{.Name}}/main.go.tmpl":   {Data: []byte("package {{.Name}}\n")},
		"{{.Name}}/README.md.tmpl": {Data: []byte("# {{.Name}}\n")},
		"{{.Name}}/wire.go.tmpl": {Data: []byte(
			"// generated {{.Name}}\n// scaffold:preserve\n")},
	}
}

// scaffoldPolicy gives each template its policy.
func scaffoldPolicy(path string) types.FilePolicy {
	switch path {
	case "svc/README.md":
		return types.PolicyOnce
	case "svc/wire.go":
		return types.PolicyPreserve
	default:
		return types.PolicyRegenerate
	}
}

// TestFoundationScaffold_PlanAndApply tests planning and applying a scaffold.
//
// Why this test is important:
//   - A scaffold rerun must update what it owns without clobbering what a developer
//     wrote: a README written once, code below a preserve marker. A rerun with nothing
//     to change must write nothing.
//
// What it tests:
//   - Templated paths render; a new file is created; a Once file that exists is kept
//     as it is; a Preserve file regenerates above its marker and keeps the tail;
//     applying writes only creates and updates; a second plan over the result keeps
//     every file and applying it writes nothing; dry run writes nothing.
func TestFoundationScaffold_PlanAndApply(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	files := map[string]string{
		"svc/README.md": "# hand written\n",
		"svc/wire.go":   "// old header\n// scaffold:preserve\nfunc custom() {}\n",
	}
	data := map[string]string{"Name": "svc"}
	plan, err := scaffold.Plan(ctx, scaffoldTemplates(), data, scaffoldPolicy,
		memoryCodebase(t, files))
	require.NoError(t, err)
	actions := map[string]types.FileAction{}
	for _, e := range plan.Entries {
		actions[e.Path] = e.Action
	}
	assert.Equal(t, map[string]types.FileAction{
		"svc/main.go": types.FileCreate, "svc/README.md": types.FileKeep,
		"svc/wire.go": types.FileUpdate,
	}, actions)

	written := map[string]string{}
	write := func(p string, b []byte) error { written[p] = string(b); return nil }
	require.NoError(t, scaffold.Apply(ctx, write, plan, true))
	assert.Empty(t, written)
	require.NoError(t, scaffold.Apply(ctx, write, plan, false))
	assert.Equal(t, map[string]string{
		"svc/main.go": "package svc\n",
		"svc/wire.go": "// generated svc\n// scaffold:preserve\nfunc custom() {}\n",
	}, written)

	maps.Copy(files, written)
	again, err := scaffold.Plan(ctx, scaffoldTemplates(), data, scaffoldPolicy,
		memoryCodebase(t, files))
	require.NoError(t, err)
	clear(written)
	require.NoError(t, scaffold.Apply(ctx, write, again, false))
	assert.Empty(t, written)
}

// TestFoundationScaffold_Conflicts tests the cases a scaffold must refuse.
//
// Why this test is important:
//   - Overwriting a preserved file that lost its marker would delete a developer's
//     code; a template referencing missing data would write a broken file.
//
// What it tests:
//   - A Preserve file without the marker is a conflict, and applying the plan writes
//     nothing and is CONFLICT naming the file; missing template data is INVALID_INPUT.
func TestFoundationScaffold_Conflicts(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	plan, err := scaffold.Plan(
		ctx,
		scaffoldTemplates(),
		map[string]string{"Name": "svc"},
		scaffoldPolicy,
		memoryCodebase(t, map[string]string{"svc/wire.go": "no marker\n"}),
	)
	require.NoError(t, err)
	written := 0
	err = scaffold.Apply(
		ctx,
		func(string, []byte) error { written++; return nil },
		plan,
		false,
	)
	assert.Equal(t, apperr.CodeConflict, apperr.Code(err))
	assert.Contains(t, err.Error(), "svc/wire.go")
	assert.Zero(t, written)

	_, err = scaffold.Plan(ctx, scaffoldTemplates(), map[string]string{},
		scaffoldPolicy, memoryCodebase(t, nil))
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestFoundationScaffold_PlanFiles tests planning content rendered by the caller.
//
// Why this test is important:
//   - A scaffold whose files are rendered by the caller (not a template tree) must get
//     the same policy decisions as Plan, so a rerun keeps an operator's file and writes
//     nothing when nothing changed.
//
// What it tests:
//   - Entries come back in path order; a missing file is created; a Once file that
//     exists is kept with its existing content; a Regenerate file is updated when it
//     differs and kept when it matches; a Preserve file without a marker conflicts.
func TestFoundationScaffold_PlanFiles(t *testing.T) {
	t.Parallel()
	cb := memoryCodebase(t, map[string]string{
		"a/hand.yaml": "tuned: true\n",
		"a/gen.hcl":   "old\n",
		"a/same.hcl":  "same\n",
		"a/keep.go":   "no marker\n",
	})
	policy := func(path string) types.FilePolicy {
		switch path {
		case "a/hand.yaml":
			return types.PolicyOnce
		case "a/keep.go":
			return types.PolicyPreserve
		default:
			return types.PolicyRegenerate
		}
	}
	plan, err := scaffold.PlanFiles(t.Context(), cb, types.Artifacts{
		"a/new.md":    []byte("new\n"),
		"a/hand.yaml": []byte("tuned: false\n"),
		"a/gen.hcl":   []byte("new\n"),
		"a/same.hcl":  []byte("same\n"),
		"a/keep.go":   []byte("head\n// scaffold:preserve\n"),
	}, policy)
	require.NoError(t, err)

	got := map[string]types.FileAction{}
	paths := make([]string, 0, len(plan.Entries))
	for _, e := range plan.Entries {
		got[e.Path] = e.Action
		paths = append(paths, e.Path)
		if e.Path == "a/hand.yaml" {
			assert.Equal(t, "tuned: true\n", string(e.Content))
		}
	}
	assert.Equal(
		t,
		[]string{
			"a/gen.hcl",
			"a/hand.yaml",
			"a/keep.go",
			"a/new.md",
			"a/same.hcl",
		},
		paths,
	)
	assert.Equal(t, map[string]types.FileAction{
		"a/new.md":    types.FileCreate,
		"a/hand.yaml": types.FileKeep,
		"a/gen.hcl":   types.FileUpdate,
		"a/same.hcl":  types.FileKeep,
		"a/keep.go":   types.FileConflict,
	}, got)
}
