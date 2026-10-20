package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stepcache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/mocks"
)

// toolrunRoot is the working-tree root the runner tests run under.
const toolrunRoot = "/repo"

// lintModules is a Go codebase with two modules.
func lintModules() map[string]string {
	return map[string]string{
		"go.work":  "go 1.27\n\nuse (\n\t./a\n\t./b\n)\n",
		"a/go.mod": "module a\n", "a/a.go": "package a\n",
		"b/go.mod": "module b\n", "b/b.go": "package b\n",
	}
}

// toolOf returns a ToolSpec mock for a binary run directly.
func toolOf(ctrl *gomock.Controller, binary string) *mocks.MockToolSpec {
	spec := mocks.NewMockToolSpec(ctrl)
	spec.EXPECT().Name().Return(binary).AnyTimes()
	spec.EXPECT().Binary().Return(binary).AnyTimes()
	spec.EXPECT().InstallHint().Return("brew install " + binary).AnyTimes()
	spec.EXPECT().Cmd(gomock.Any()).DoAndReturn(func(args ...string) (string, []string) {
		return binary, args
	}).AnyTimes()
	return spec
}

// newToolRunner builds a runner whose only tool is spec.
func newToolRunner(
	t *testing.T,
	runner interfaces.CommandRunner,
	spec interfaces.ToolSpec,
	cache interfaces.StepCache,
) interfaces.ToolRunner {
	t.Helper()
	tr, err := toolrun.NewFromConfig(toolrun.Config{}, toolrun.Deps{
		Decorations: fixtures.Decorations(),
		Runner:      runner,
		Root:        toolrunRoot,
		Cache:       cache,
		Tools: func(name string) (interfaces.ToolSpec, bool) {
			return spec, name == spec.Name()
		},
		Versions: func(string) string { return "v1" },
	})
	require.NoError(t, err)
	return tr
}

// TestServicesToolrun_MissingAndUnknownTool tests the presence check.
//
// Why this test is important:
//   - A developer without a tool installed should see one clear step with the install
//     hint, not a wall of exec failures; CI can make that a failure; a row naming a
//     tool the toolchain does not know is a manifest bug.
//
// What it tests:
//   - A missing tool is one skipped step carrying the install hint by default and one
//     failed step under missing: fail; an unknown tool is INVALID_INPUT.
func TestServicesToolrun_MissingAndUnknownTool(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("hadolint").Return(false).AnyTimes()
	tr := newToolRunner(t, runner, toolOf(ctrl, "hadolint"), nil)
	cb := memoryCodebase(t, map[string]string{"Dockerfile": "FROM x\n"})
	run := types.ToolRun{Name: "hadolint", Verb: "lint", Tool: "hadolint"}

	res, err := tr.Run(t.Context(), cb, run, false)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Contains(t, res[0].Error, "brew install hadolint")

	run.Missing = types.MissingFail
	res, err = tr.Run(t.Context(), cb, run, false)
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	_, err = tr.Run(t.Context(), cb, types.ToolRun{Name: "x", Tool: "nope"}, false)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestServicesToolrun_EachTargetFansOut tests per-target runs.
//
// Why this test is important:
//   - A per-module linter must run once in every module, in that module's directory,
//     with --fix appended only when asked; one step per module tells the reader which
//     module failed.
//
// What it tests:
//   - each: target over go_modules with cwd: target runs the tool once per module in
//     the module's directory with `{target}` as ".", appends fix args, and returns one
//     step per module named after the run and the module; a failing module fails only
//     its own step.
func TestServicesToolrun_EachTargetFansOut(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("golangci-lint").Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, "a"),
		"golangci-lint", "run", ".", "--fix").Return(interfaces.CmdResult{})
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, "b"),
		"golangci-lint", "run", ".", "--fix").Return(interfaces.CmdResult{
		ExitCode: 1,
		Err:      apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout:   []byte("bad"),
	})
	tr := newToolRunner(t, runner, toolOf(ctrl, "golangci-lint"), nil)
	res, err := tr.Run(t.Context(), memoryCodebase(t, lintModules()), types.ToolRun{
		Name: "golangci-lint", Verb: "lint", Tool: "golangci-lint",
		Args: []string{"run", "{target}"}, FixArgs: []string{"--fix"},
		Targets: types.TargetSpec{Kind: "go_modules"},
		Each:    types.EachTarget, Cwd: types.CwdTarget,
	}, true)
	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, "golangci-lint a", res[0].Name)
	assert.Equal(t, "lint", res[0].Group)
	assert.Equal(t, types.StatusPass, res[0].Status)
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Equal(t, "bad", res[1].Error)
}

// TestServicesToolrun_EachRunAndParse tests a single run over every target.
//
// Why this test is important:
//   - Tools that take many files at once (shellcheck, hadolint) run once with every
//     target; a parse kind turns their output into findings attributed to the run.
//
// What it tests:
//   - each: run expands `{target}` to every selected file in the root and returns one
//     step; the gcc parser's findings carry the run's name as their check.
func TestServicesToolrun_EachRunAndParse(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("shellcheck").Return(true)
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, "shellcheck", "-f", "gcc",
		"a.sh", "b/c.sh").Return(interfaces.CmdResult{
		ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout: []byte("a.sh:3:1: warning: quote this\n"),
	})
	tr := newToolRunner(t, runner, toolOf(ctrl, "shellcheck"), nil)
	res, err := tr.Run(t.Context(),
		memoryCodebase(t, map[string]string{"a.sh": "", "b/c.sh": "", "d.md": ""}),
		types.ToolRun{
			Name: "shellcheck", Verb: "lint", Tool: "shellcheck", Parse: "gcc",
			Args:    []string{"-f", "gcc", "{target}"},
			Targets: types.TargetSpec{Kind: "files", Glob: "**/*.sh"},
		}, false)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "shellcheck", res[0].Name)
	assert.Equal(t, types.StatusFail, res[0].Status)
	assert.Equal(t, "1 finding", res[0].Error)
	assert.Equal(t, "a.sh:3: warning: quote this\n", string(res[0].Detail)[:28])
}

// TestServicesToolrun_Cache tests the step cache.
//
// Why this test is important:
//   - Skipping an unchanged module's lint is the speedup; skipping one whose inputs
//     changed would report a pass nobody ran.
//
// What it tests:
//   - A passing run records its key; the same inputs skip with detail "cached" without
//     running the tool; one changed input byte runs the tool again; a run with no
//     inputs is never cached.
func TestServicesToolrun_Cache(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cache, err := stepcache.NewFromConfig(stepcache.Config{
		Kind: stepcache.KindDir, Dir: t.TempDir(),
	})
	require.NoError(t, err)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("vet").Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "vet", gomock.Any()).
		Return(interfaces.CmdResult{}).Times(3)
	tr := newToolRunner(t, runner, toolOf(ctrl, "vet"), cache)
	run := types.ToolRun{
		Name: "vet", Verb: "lint", Tool: "vet", Args: []string{"{target}"},
		Targets: types.TargetSpec{Kind: "go_modules"}, Each: types.EachTarget,
		Inputs: types.FileSelector{Roots: []string{"{target}"}, Exts: []string{".go"}},
	}

	files := lintModules()
	res, err := tr.Run(t.Context(), memoryCodebase(t, files), run, false)
	require.NoError(t, err)
	assert.Equal(t, []types.StepStatus{types.StatusPass, types.StatusPass},
		[]types.StepStatus{res[0].Status, res[1].Status})

	files["b/b.go"] = "package b // changed\n"
	res, err = tr.Run(t.Context(), memoryCodebase(t, files), run, false)
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Equal(t, "cached", string(res[0].Detail))
	assert.Equal(t, types.StatusPass, res[1].Status)

	uncached := run
	uncached.Inputs = types.FileSelector{}
	uncached.Each = types.EachRun
	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "vet", "a", "b").
		Return(interfaces.CmdResult{}).Times(2)
	for range 2 {
		res, err = tr.Run(t.Context(), memoryCodebase(t, files), uncached, false)
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status)
	}
}
