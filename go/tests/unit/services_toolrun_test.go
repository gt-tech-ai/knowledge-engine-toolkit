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
//     failed step under missing: fail; an unknown tool is INVALID_INPUT; a prefixed
//     tool (`uv run ruff`) runs when its prefix is installed.
func TestServicesToolrun_MissingAndUnknownTool(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("hadolint").Return(false).AnyTimes()
	tr := newToolRunner(t, runner, toolOf(ctrl, "hadolint"), nil)
	cb := memoryCodebase(t, map[string]string{"Dockerfile": "FROM x\n"})
	run := types.ToolRun{Name: "hadolint", Verb: "lint", Tool: "hadolint"}

	res, err := tr.Run(t.Context(), cb, run, types.RunOptions{})
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Contains(t, res[0].Error, "brew install hadolint")

	run.Missing = types.MissingFail
	res, err = tr.Run(t.Context(), cb, run, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	_, err = tr.Run(
		t.Context(),
		cb,
		types.ToolRun{Name: "x", Tool: "nope"},
		types.RunOptions{},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	ruff := mocks.NewMockToolSpec(ctrl)
	ruff.EXPECT().Name().Return("ruff").AnyTimes()
	ruff.EXPECT().Binary().Return("ruff").AnyTimes()
	ruff.EXPECT().Cmd(gomock.Any()).DoAndReturn(func(args ...string) (string, []string) {
		return "uv", append([]string{"run", "ruff"}, args...)
	}).AnyTimes()
	runner.EXPECT().Exists("uv").Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "uv", "run", "ruff", "check").
		Return(interfaces.CmdResult{})
	res, err = newToolRunner(t, runner, ruff, nil).Run(t.Context(), cb,
		types.ToolRun{Name: "ruff", Verb: "lint", Tool: "ruff", Args: []string{"check"}},
		types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(
		t,
		types.StatusPass,
		res[0].Status,
		"a prefixed tool needs only its prefix",
	)
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
	}, types.RunOptions{Fix: true})
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
		}, types.RunOptions{})
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
//     inputs is never cached; under the off cache a step never skips and no tool
//     version is probed.
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
	res, err := tr.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []types.StepStatus{types.StatusPass, types.StatusPass},
		[]types.StepStatus{res[0].Status, res[1].Status})

	files["b/b.go"] = "package b // changed\n"
	res, err = tr.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
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
		res, err = tr.Run(
			t.Context(),
			memoryCodebase(t, files),
			uncached,
			types.RunOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status)
	}

	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), "vet", gomock.Any()).
		Return(interfaces.CmdResult{}).Times(4)
	off, err := toolrun.NewFromConfig(toolrun.Config{}, toolrun.Deps{
		Decorations: fixtures.Decorations(),
		Runner:      runner,
		Root:        toolrunRoot,
		Tools: func(name string) (interfaces.ToolSpec, bool) {
			return toolOf(ctrl, "vet"), name == "vet"
		},
		Versions: func(string) string {
			t.Error("the off cache probed a tool version")
			return ""
		},
	})
	require.NoError(t, err)
	for range 2 {
		res, err = off.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status, "the off cache never skips")
	}
}

// TestServicesToolrun_NoTargetsSkips tests a selector that selects nothing.
//
// Why this test is important:
//   - A run over every shell script in a repository with none must not run the linter
//     with no arguments, which for most tools means "read stdin" or "check the current
//     directory"; it has nothing to check.
//
// What it tests:
//   - A selector that picks no files is one skipped step with detail "no targets" under
//     both each: target and each: run, and the tool never runs.
func TestServicesToolrun_NoTargetsSkips(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("shellcheck").Return(true).AnyTimes()
	tr := newToolRunner(t, runner, toolOf(ctrl, "shellcheck"), nil)
	for _, each := range []string{types.EachTarget, types.EachRun} {
		res, err := tr.Run(t.Context(), memoryCodebase(t, map[string]string{"a.md": ""}),
			types.ToolRun{
				Name: "shellcheck", Verb: "lint", Tool: "shellcheck",
				Args:    []string{"{target}"},
				Targets: types.TargetSpec{Kind: "files", Glob: "**/*.sh"},
				Each:    each,
			}, types.RunOptions{})
		require.NoError(t, err)
		require.Len(t, res, 1, each)
		assert.Equal(t, types.StatusSkip, res[0].Status, each)
		assert.Equal(t, "no targets", string(res[0].Detail), each)
	}
}

// TestServicesToolrun_GroupCwdAndAdvisory tests a run's phase, working directory and
// advisory mode.
//
// Why this test is important:
//   - A command's rows render under the gate phase they belong to, not the command's
//     verb; a tool that must run in a fixed directory (buf in the proto dir) needs it as
//     data; an advisory check reports a problem without failing the run, and a result
//     nobody passed must never be replayed from the cache.
//
// What it tests:
//   - A grouped run's step carries the group and an ungrouped one keeps the verb; a
//     repo-relative cwd runs the tool there; an advisory failure is a warning with the
//     tool's output and is not recorded, so the next run invokes the tool again; a cwd
//     escaping the root is INVALID_INPUT.
func TestServicesToolrun_GroupCwdAndAdvisory(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cache, err := stepcache.NewFromConfig(stepcache.Config{
		Kind: stepcache.KindDir, Dir: t.TempDir(),
	})
	require.NoError(t, err)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("buf").Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, "zarf", "proto"),
		"buf", "breaking").Return(interfaces.CmdResult{
		ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout: []byte("removed field"),
	}).Times(2)
	tr := newToolRunner(t, runner, toolOf(ctrl, "buf"), cache)
	cb := memoryCodebase(
		t,
		map[string]string{"zarf/proto/a.proto": "syntax = \"proto3\";\n"},
	)
	run := types.ToolRun{
		Name: "breaking", Verb: "check", Tool: "buf", Group: "Proto",
		Cwd: "zarf/proto", Args: []string{"breaking"}, Advisory: true,
		Inputs: types.FileSelector{Roots: []string{"zarf/proto"}},
	}

	for range 2 {
		res, err := tr.Run(t.Context(), cb, run, types.RunOptions{})
		require.NoError(t, err)
		require.Len(t, res, 1)
		assert.Equal(t, "Proto", res[0].Group)
		assert.Equal(t, types.StatusWarn, res[0].Status)
		assert.Equal(t, "removed field", res[0].Error)
	}

	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, "buf", "lint").
		Return(interfaces.CmdResult{})
	res, err := tr.Run(t.Context(), cb, types.ToolRun{
		Name: "lint", Verb: "check", Tool: "buf", Args: []string{"lint"},
	}, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, "check", res[0].Group)

	_, err = tr.Run(t.Context(), cb, types.ToolRun{
		Name: "x", Verb: "check", Tool: "buf", Cwd: "../elsewhere",
	}, types.RunOptions{})
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestServicesToolrun_RunOptions tests the per-invocation options.
//
// Why this test is important:
//   - CI must fail when a tool is missing even where a developer's run skips it; a
//     scanner's per-invocation value (the image, the commit range) comes from a flag, and
//     a row that needs one must refuse to run without it rather than scan nothing; a
//     cached pass for one image must not answer for another.
//
// What it tests:
//   - Strict turns a missing tool into a failure; `{image}` takes Vars["image"] in args
//     and fix args; a placeholder with no value is INVALID_INPUT naming the run and the
//     variable; changing a var's value misses the cache.
func TestServicesToolrun_RunOptions(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	cache, err := stepcache.NewFromConfig(stepcache.Config{
		Kind: stepcache.KindDir, Dir: t.TempDir(),
	})
	require.NoError(t, err)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists("trivy").Return(true).AnyTimes()
	runner.EXPECT().Exists("hadolint").Return(false).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, "trivy", "image", "api:1",
		"--exit-code", "1").Return(interfaces.CmdResult{})
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, "trivy", "image", "api:2",
		"--exit-code", "1").Return(interfaces.CmdResult{})
	spec := toolOf(ctrl, "trivy")
	tr, err := toolrun.NewFromConfig(toolrun.Config{}, toolrun.Deps{
		Decorations: fixtures.Decorations(), Runner: runner, Root: toolrunRoot,
		Cache: cache,
		Tools: func(name string) (interfaces.ToolSpec, bool) {
			if name == "hadolint" {
				return toolOf(ctrl, "hadolint"), true
			}
			return spec, name == "trivy"
		},
	})
	require.NoError(t, err)
	cb := memoryCodebase(t, map[string]string{"Dockerfile": "FROM x\n"})

	res, err := tr.Run(t.Context(), cb,
		types.ToolRun{Name: "hadolint", Verb: "lint", Tool: "hadolint"},
		types.RunOptions{Strict: true})
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	image := types.ToolRun{
		Name: "trivy image", Verb: "security", Tool: "trivy",
		Args: []string{"image", "{image}"}, FixArgs: []string{"--exit-code", "1"},
		Inputs: types.FileSelector{Paths: []string{"Dockerfile"}},
	}
	for _, img := range []string{"api:1", "api:2"} {
		res, err = tr.Run(t.Context(), cb, image, types.RunOptions{
			Fix: true, Vars: map[string]string{"image": img},
		})
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status, img)
	}

	_, err = tr.Run(t.Context(), cb, image, types.RunOptions{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "trivy image")
	assert.ErrorContains(t, err, "image")
}
