package unit_test

import (
	"maps"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
	case textSvcReadmeMd:
		return types.PolicyOnce
	case textSvcWireGo:
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
		textSvcReadmeMd: "# hand written\n",
		textSvcWireGo:   "// old header\n// scaffold:preserve\nfunc custom() {}\n",
	}
	data := map[string]string{textNameTitle: textSvc}
	plan, err := scaffold.Plan(ctx, scaffoldTemplates(), data, scaffoldPolicy,
		memoryCodebase(t, files))
	require.NoError(t, err)

	actions := map[string]types.FileAction{}
	for _, e := range plan.Entries {
		actions[e.Path] = e.Action
	}

	assert.Equal(t, map[string]types.FileAction{
		"svc/main.go": types.FileCreate, textSvcReadmeMd: types.FileKeep,
		textSvcWireGo: types.FileUpdate,
	}, actions)

	written := map[string]string{}
	write := func(p string, b []byte) error {
		written[p] = string(b)

		return nil
	}

	require.NoError(t, scaffold.Conflicts(plan))
	assert.Empty(t, written)
	require.NoError(t, scaffold.Apply(ctx, write, plan))
	assert.Equal(t, map[string]string{
		"svc/main.go": "package svc\n",
		textSvcWireGo: "// generated svc\n// scaffold:preserve\nfunc custom() {}\n",
	}, written)

	maps.Copy(files, written)

	again, err := scaffold.Plan(ctx, scaffoldTemplates(), data, scaffoldPolicy,
		memoryCodebase(t, files))
	require.NoError(t, err)
	clear(written)
	require.NoError(t, scaffold.Apply(ctx, write, again))
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
		map[string]string{textNameTitle: textSvc},
		scaffoldPolicy,
		memoryCodebase(t, map[string]string{textSvcWireGo: "no marker\n"}),
	)
	require.NoError(t, err)

	written := 0

	err = scaffold.Apply(
		ctx,
		func(string, []byte) error {
			written++

			return nil
		},
		plan,
	)
	assert.Equal(t, apperr.CodeConflict, apperr.Code(err))
	assert.Contains(t, err.Error(), textSvcWireGo)
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

	codebase := memoryCodebase(t, map[string]string{
		textAHandYaml: "tuned: true\n",
		textAGenHcl:   textOld,
		textASameHcl:  "same\n",
		textAKeepGo:   "no marker\n",
	})
	policy := func(path string) types.FilePolicy {
		switch path {
		case textAHandYaml:
			return types.PolicyOnce
		case textAKeepGo:
			return types.PolicyPreserve
		default:
			return types.PolicyRegenerate
		}
	}
	plan, err := scaffold.PlanFiles(t.Context(), codebase, types.Artifacts{
		textANewMd:    []byte("new\n"),
		textAHandYaml: []byte("tuned: false\n"),
		textAGenHcl:   []byte("new\n"),
		textASameHcl:  []byte("same\n"),
		textAKeepGo:   []byte("head\n// scaffold:preserve\n"),
	}, policy)
	require.NoError(t, err)

	got := map[string]types.FileAction{}
	paths := make([]string, 0, len(plan.Entries))

	for _, e := range plan.Entries {
		got[e.Path] = e.Action
		paths = append(paths, e.Path)

		if e.Path == textAHandYaml {
			assert.Equal(t, "tuned: true\n", string(e.Content))
		}
	}

	assert.Equal(
		t,
		[]string{
			textAGenHcl,
			textAHandYaml,
			textAKeepGo,
			textANewMd,
			textASameHcl,
		},
		paths,
	)
	assert.Equal(t, map[string]types.FileAction{
		textANewMd:    types.FileCreate,
		textAHandYaml: types.FileKeep,
		textAGenHcl:   types.FileUpdate,
		textASameHcl:  types.FileKeep,
		textAKeepGo:   types.FileConflict,
	}, got)
}

const (
	// textAGenHcl is the literal "a/gen.hcl".
	textAGenHcl = "a/gen.hcl"

	// textAHandYaml is the literal "a/hand.yaml".
	textAHandYaml = "a/hand.yaml"

	// textAKeepGo is the literal "a/keep.go".
	textAKeepGo = "a/keep.go"

	// textANewMd is the literal "a/new.md".
	textANewMd = "a/new.md"

	// textASameHcl is the literal "a/same.hcl".
	textASameHcl = "a/same.hcl"

	// textSvcReadmeMd is the literal "svc/README.md".
	textSvcReadmeMd = "svc/README.md"

	// textSvcWireGo is the literal "svc/wire.go".
	textSvcWireGo = "svc/wire.go"
)
