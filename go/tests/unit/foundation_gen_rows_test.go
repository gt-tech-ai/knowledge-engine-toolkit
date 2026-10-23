package unit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/gen"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// TestFoundationGen_RunRow tests a generator that runs a tool.
//
// Why this test is important:
//   - code generators (buf, mockgen, swagger) become drift-checked generators as data
//     only if the tool's output directory is captured instead of the working tree.
//
// What it tests:
//   - the tool runs with {out} set to a scratch directory; every file it writes there
//     is an artifact under Out; gen.Check reports the stale committed file as drift;
//     a failing run is an error.
func TestFoundationGen_RunRow(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	tools := mocks.NewMockToolRunner(ctrl)
	row := types.GenRow{Name: "proto", Kind: types.GenRun, Out: "gen/go",
		Run: types.ToolRun{Name: "buf", Tool: "buf", Args: []string{"generate", "-o", "{out}"}}}
	tools.EXPECT().Run(gomock.Any(), gomock.Any(), row.Run, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ interfaces.Codebase, _ types.ToolRun,
			opts types.RunOptions,
		) (types.StepResults, error) {
			dir := filepath.Join(opts.Vars["out"], "v1")
			require.NoError(t, os.MkdirAll(dir, 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "a.pb.go"), []byte("new\n"), 0o600))
			return types.OneStep("buf", types.StatusPass, nil), nil
		}).Times(2)
	g, err := gen.NewFromRow(row, gen.Deps{Tools: tools})
	require.NoError(t, err)
	cb := memoryCodebase(t, map[string]string{"gen/go/v1/a.pb.go": "old\n"})

	out, err := g.Render(t.Context(), cb)
	require.NoError(t, err)
	assert.Equal(t, types.Artifacts{"gen/go/v1/a.pb.go": []byte("new\n")}, out)
	d, err := gen.Check(t.Context(), cb, g)
	require.NoError(t, err)
	assert.Equal(t, []string{"gen/go/v1/a.pb.go"}, d.Paths)

	tools.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(types.OneStep("buf", types.StatusFail, nil), nil)
	_, err = g.Render(t.Context(), cb)
	require.Error(t, err)
}

// TestFoundationGen_TemplateRow tests a generator that renders a template.
//
// Why this test is important:
//   - generated docs and config (a reference table, a dependency rule list) are a
//     template over data; a missing key must fail rather than render "<no value>".
//
// What it tests:
//   - the repository template renders the provider's data into Out; a missing key and
//     an unknown kind or provider are errors.
func TestFoundationGen_TemplateRow(t *testing.T) {
	t.Parallel()
	data := map[string]gen.DataFunc{"svcs": func(context.Context, interfaces.Codebase) (any, error) {
		return map[string]any{"Names": []string{"api", "web"}}, nil
	}}
	cb := memoryCodebase(t, map[string]string{
		"t/list.tmpl": "{{range .Names}}- {{.}}\n{{end}}", "t/bad.tmpl": "{{.Missing}}",
	})
	g, err := gen.NewFromRow(types.GenRow{Name: "list", Kind: types.GenTemplate,
		Template: "t/list.tmpl", Data: "svcs", Out: "docs/list.md"}, gen.Deps{Data: data})
	require.NoError(t, err)
	out, err := g.Render(t.Context(), cb)
	require.NoError(t, err)
	assert.Equal(t, "- api\n- web\n", string(out["docs/list.md"]))

	bad, err := gen.NewFromRow(types.GenRow{Name: "bad", Kind: types.GenTemplate,
		Template: "t/bad.tmpl", Data: "svcs", Out: "x"}, gen.Deps{Data: data})
	require.NoError(t, err)
	_, err = bad.Render(t.Context(), cb)
	require.Error(t, err)

	for _, r := range []types.GenRow{
		{Name: "k", Kind: "copy"},
		{Name: "r", Kind: types.GenRun},
		{Name: "d", Kind: types.GenTemplate, Template: "t/list.tmpl", Data: "nope"},
	} {
		_, err := gen.NewFromRow(r, gen.Deps{Data: data})
		require.Error(t, err, r.Name)
	}
}

// TestCoreTypes_MergeEnv tests environment merging.
//
// Why this test is important:
//   - a tool must run without an inherited variable (GOFLAGS, VIRTUAL_ENV) as reliably
//     as with an added one, and a set must replace, not duplicate, the inherited value.
//
// What it tests:
//   - NAME=value replaces or appends, -NAME removes, other entries keep their order.
func TestCoreTypes_MergeEnv(t *testing.T) {
	t.Parallel()
	assert.Equal(t,
		[]string{"PATH=/bin", "HOME=/h", "GOFLAGS=-mod=mod", "NEW=1"},
		types.MergeEnv(
			[]string{"PATH=/bin", "VIRTUAL_ENV=/v", "GOFLAGS=x", "HOME=/h"},
			[]string{"-VIRTUAL_ENV", "GOFLAGS=-mod=mod", "NEW=1", "-ABSENT"}))
	assert.Equal(t, []string{"A=1"}, types.MergeEnv([]string{"A=1"}, nil))
}
