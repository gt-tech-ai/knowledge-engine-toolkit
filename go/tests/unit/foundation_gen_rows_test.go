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

// TestFoundationGen_TemplateRowEach tests a template row that renders one file per
// data element.
//
// Why this test is important:
//   - per-service artifacts (a Dockerfile per service, a chart per app) are one
//     template over a list; the row, not Go, must say where each lands and which
//     template each takes, or every such generator stays hand-written.
//
// What it tests:
//   - each element renders the template its own `template` names into the file its
//     own `out` names; data that is not a list, and two elements naming one output,
//     are errors.
func TestFoundationGen_TemplateRowEach(t *testing.T) {
	t.Parallel()
	type svc struct{ Name, Lang string }
	list := func(v any) gen.DataFunc {
		return func(context.Context, interfaces.Codebase) (any, error) { return v, nil }
	}
	cb := memoryCodebase(t, map[string]string{
		"t/go.tmpl": "go {{.Name}}\n", "t/py.tmpl": "py {{.Name}}\n",
	})
	row := types.GenRow{Name: "images", Kind: types.GenTemplate, Each: true,
		Template: "t/{{.Lang}}.tmpl", Data: "svcs", Out: "docker/{{.Name}}/Dockerfile"}
	render := func(data any) (types.Artifacts, error) {
		g, err := gen.NewFromRow(row, gen.Deps{Data: map[string]gen.DataFunc{"svcs": list(data)}})
		require.NoError(t, err)
		return g.Render(t.Context(), cb)
	}

	out, err := render([]svc{{"api", "go"}, {"ingest", "py"}})
	require.NoError(t, err)
	assert.Equal(t, types.Artifacts{
		"docker/api/Dockerfile":    []byte("go api\n"),
		"docker/ingest/Dockerfile": []byte("py ingest\n"),
	}, out)

	_, err = render(svc{"api", "go"})
	require.Error(t, err, "not a list")
	_, err = render([]svc{{"api", "go"}, {"api", "py"}})
	require.Error(t, err, "one output twice")
}

// TestFoundationGen_TemplateRowRegion tests a template row that fills a marked region
// of an existing file.
//
// Why this test is important:
//   - generated rules often live inside a hand-kept config (a linter's rule list); the
//     row must replace only the marked block and keep the rest byte for byte, or the
//     hand-kept part is lost on the next generate.
//
// What it tests:
//   - the rendered block replaces the lines from the begin marker through the end
//     marker, keeping the text around them; a file missing a marker is an error.
func TestFoundationGen_TemplateRowRegion(t *testing.T) {
	t.Parallel()
	data := map[string]gen.DataFunc{"rules": func(context.Context, interfaces.Codebase) (any, error) {
		return []string{"a", "b"}, nil
	}}
	row := types.GenRow{Name: "rules", Kind: types.GenTemplate, Template: "t/rules.tmpl",
		Data: "rules", Out: "lint.yml",
		Region: types.GenRegion{Begin: "# BEGIN rules", End: "# END rules"}}
	tmpl := "  # BEGIN rules\n{{range .}}  - {{.}}\n{{end}}  # END rules\n"
	g, err := gen.NewFromRow(row, gen.Deps{Data: data})
	require.NoError(t, err)

	cb := memoryCodebase(t, map[string]string{"t/rules.tmpl": tmpl,
		"lint.yml": "head:\n  # BEGIN rules\n  - old\n  # END rules\ntail: 1\n"})
	out, err := g.Render(t.Context(), cb)
	require.NoError(t, err)
	assert.Equal(t, "head:\n  # BEGIN rules\n  - a\n  - b\n  # END rules\ntail: 1\n",
		string(out["lint.yml"]))

	_, err = g.Render(t.Context(), memoryCodebase(t, map[string]string{"t/rules.tmpl": tmpl,
		"lint.yml": "head:\n  # BEGIN rules\n"}))
	require.Error(t, err, "no end marker")
}

// TestFoundationGen_EditRow tests a generator that sets values in existing YAML files.
//
// Why this test is important:
//   - a value projected from data into a hand-kept file (a distribution id in a chart's
//     values) must change only its own token: the comments, quoting and every other
//     line the file's owner wrote survive; and a file that no longer declares the key
//     must fail rather than silently project nothing.
//
// What it tests:
//   - each element sets its paths in the file its out names, keeping the token's double
//     quotes, its trailing comment and every other line; a path that selects no scalar
//     is an error; an edit row needs a set.
func TestFoundationGen_EditRow(t *testing.T) {
	t.Parallel()
	type env struct{ Path, ID string }
	data := func(v any) map[string]gen.DataFunc {
		return map[string]gen.DataFunc{"envs": func(context.Context, interfaces.Codebase) (any, error) {
			return v, nil
		}}
	}
	row := types.GenRow{Name: "cdn", Kind: types.GenEdit, Each: true, Data: "envs",
		Out: "{{.Path}}", Set: map[string]string{"$.cdn.id": "{{.ID}}"}}
	src := "# top\ncdn:\n  id: \"\"  # set by gen\n  keep: 1\nother: x\n"
	cb := memoryCodebase(t, map[string]string{"a/values.yaml": src, "b/values.yaml": "other: x\n"})

	g, err := gen.NewFromRow(row, gen.Deps{Data: data([]env{{"a/values.yaml", "E123"}})})
	require.NoError(t, err)
	out, err := g.Render(t.Context(), cb)
	require.NoError(t, err)
	assert.Equal(t, "# top\ncdn:\n  id: \"E123\"  # set by gen\n  keep: 1\nother: x\n",
		string(out["a/values.yaml"]))

	g, err = gen.NewFromRow(row, gen.Deps{Data: data([]env{{"b/values.yaml", "E123"}})})
	require.NoError(t, err)
	_, err = g.Render(t.Context(), cb)
	require.Error(t, err, "the file declares no cdn.id")

	_, err = gen.NewFromRow(types.GenRow{Name: "e", Kind: types.GenEdit, Data: "envs", Out: "x"},
		gen.Deps{Data: data(nil)})
	require.Error(t, err, "an edit row needs a set")
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
