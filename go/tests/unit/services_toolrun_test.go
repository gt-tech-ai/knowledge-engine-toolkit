package unit_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
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
		textGoWork: "go 1.27\n\nuse (\n\t./a\n\t./b\n)\n",
		"a/go.mod": textModuleA, "a/a.go": textPackageA,
		"b/go.mod": textModuleB, "b/b.go": "package b\n",
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
		Versions: func(string) string { return textV1 },
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
	runner.EXPECT().Exists(textHadolint).Return(false).AnyTimes()

	tr := newToolRunner(t, runner, toolOf(ctrl, textHadolint), nil)
	cb := memoryCodebase(t, map[string]string{textDockerfile: "FROM x\n"})
	run := types.ToolRun{Name: textHadolint, Verb: textLint, Tool: textHadolint}

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
		types.ToolRun{Name: textX, Tool: textNope},
		types.RunOptions{},
	)
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))

	ruff := mocks.NewMockToolSpec(ctrl)
	ruff.EXPECT().Name().Return(textRuff).AnyTimes()
	ruff.EXPECT().Binary().Return(textRuff).AnyTimes()
	ruff.EXPECT().Cmd(gomock.Any()).DoAndReturn(func(args ...string) (string, []string) {
		return textUv, append([]string{textRun, textRuff}, args...)
	}).AnyTimes()
	runner.EXPECT().Exists(textUv).Return(true).AnyTimes()
	runner.EXPECT().
		RunBuffered(gomock.Any(), gomock.Any(), textUv, textRun, textRuff, textCheck).
		Return(interfaces.CmdResult{})

	res, err = newToolRunner(t, runner, ruff, nil).Run(t.Context(), cb,
		types.ToolRun{
			Name: textRuff,
			Verb: textLint,
			Tool: textRuff,
			Args: []string{textCheck},
		},
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
	runner.EXPECT().Exists(textGolangciLint).Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, textA),
		textGolangciLint, textRun, ".", textFix2).Return(interfaces.CmdResult{})
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, textB),
		textGolangciLint, textRun, ".", textFix2).Return(interfaces.CmdResult{
		ExitCode: 1,
		Err:      apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout:   []byte(textBad),
	})

	tr := newToolRunner(t, runner, toolOf(ctrl, textGolangciLint), nil)
	res, err := tr.Run(t.Context(), memoryCodebase(t, lintModules()), types.ToolRun{
		Name: textGolangciLint, Verb: textLint, Tool: textGolangciLint,
		Args: []string{textRun, textTarget2}, FixArgs: []string{textFix2},
		Targets: types.TargetSpec{Kind: "go_modules"},
		Each:    types.EachTarget, Cwd: types.CwdTarget,
	}, types.RunOptions{Fix: true})
	require.NoError(t, err)
	require.Len(t, res, wantResLen2)
	assert.Equal(t, "golangci-lint a", res[0].Name)
	assert.Equal(t, textLint, res[0].Group)
	assert.Equal(t, types.StatusPass, res[0].Status)
	assert.Equal(t, types.StatusFail, res[1].Status)
	assert.Equal(t, textBad, res[1].Error)
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
	runner.EXPECT().Exists(textShellcheck).Return(true)
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, textShellcheck, "-f", textGcc,
		"a.sh", "b/c.sh").Return(interfaces.CmdResult{
		ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout: []byte("a.sh:3:1: warning: quote this\n"),
	})

	tr := newToolRunner(t, runner, toolOf(ctrl, textShellcheck), nil)
	res, err := tr.Run(t.Context(),
		memoryCodebase(t, map[string]string{"a.sh": "", "b/c.sh": "", "d.md": ""}),
		types.ToolRun{
			Name: textShellcheck, Verb: textLint, Tool: textShellcheck, Parse: textGcc,
			Args:    []string{"-f", textGcc, textTarget2},
			Targets: types.TargetSpec{Kind: textFiles, Glob: "**/*.sh"},
		}, types.RunOptions{})
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, textShellcheck, res[0].Name)
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
	runner.EXPECT().Exists(textVet).Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), textVet, gomock.Any()).
		Return(interfaces.CmdResult{}).Times(timesArg3)

	tr := newToolRunner(t, runner, toolOf(ctrl, textVet), cache)
	run := types.ToolRun{
		Name: textVet, Verb: textLint, Tool: textVet, Args: []string{textTarget2},
		Targets: types.TargetSpec{Kind: "go_modules"}, Each: types.EachTarget,
		Inputs: types.FileSelector{Roots: []string{textTarget2}, Exts: []string{textGo3}},
	}

	t.Helper()

	testServicesToolrunCacheRest3(t, ctrl, runner, tr, run)
}

// testServicesToolrunCacheRest3 continues testServicesToolrunCacheRest2 with its
// remaining
// steps.
func testServicesToolrunCacheRest3(
	t *testing.T,
	ctrl *gomock.Controller,
	runner *mocks.MockCommandRunner,
	tr interfaces.ToolRunner,
	run types.ToolRun,
) {
	t.Helper()

	var err error

	files := lintModules()
	res, err := tr.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, []types.StepStatus{types.StatusPass, types.StatusPass},
		[]types.StepStatus{res[0].Status, res[1].Status})

	files["b/b.go"] = "package b // changed\n"
	res, err = tr.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusSkip, res[0].Status)
	assert.Equal(t, textCached, string(res[0].Detail))
	assert.Equal(t, types.StatusPass, res[1].Status)

	uncached := run

	uncached.Inputs = types.FileSelector{}
	uncached.Each = types.EachRun

	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), textVet, textA, textB).
		Return(interfaces.CmdResult{}).Times(timesArg2)

	for range iterations2 {
		res, err = tr.Run(
			t.Context(),
			memoryCodebase(t, files),
			uncached,
			types.RunOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status)
	}

	testServicesToolrunCacheRest4(t, ctrl, runner, run, files)
}

// testServicesToolrunCacheRest4 continues testServicesToolrunCacheRest3 with its
// remaining
// steps.
func testServicesToolrunCacheRest4(
	t *testing.T,
	ctrl *gomock.Controller,
	runner *mocks.MockCommandRunner,
	run types.ToolRun,
	files map[string]string,
) {
	t.Helper()

	var res types.StepResults

	var err error

	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), textVet, gomock.Any()).
		Return(interfaces.CmdResult{}).Times(timesArg4)

	off, err := toolrun.NewFromConfig(toolrun.Config{}, toolrun.Deps{
		Decorations: fixtures.Decorations(),
		Runner:      runner,
		Root:        toolrunRoot,
		Tools: func(name string) (interfaces.ToolSpec, bool) {
			return toolOf(ctrl, textVet), name == textVet
		},
		Versions: func(string) string {
			t.Error("the off cache probed a tool version")

			return ""
		},
	})
	require.NoError(t, err)

	for range iterations2 {
		res, err = off.Run(t.Context(), memoryCodebase(t, files), run, types.RunOptions{})
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status, "the off cache never skips")
	}
}

// TestServicesToolrun_RunEditMissesCache tests that a run's cache key covers the run's
// whole definition.
//
// Why this test is important:
//   - a run's environment, parser or advisory flag changes what a pass means as much as
//     its arguments do; a key over the arguments alone replays the old verdict.
//
// What it tests:
//   - a cached run edited only in its environment runs the tool again, and its own pass
//     is then cached.
func TestServicesToolrun_RunEditMissesCache(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	cache, err := stepcache.NewFromConfig(stepcache.Config{
		Kind: stepcache.KindDir, Dir: t.TempDir(),
	})
	require.NoError(t, err)

	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists(textVet).Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), gomock.Any(), textVet, gomock.Any()).
		Return(interfaces.CmdResult{}).Times(1)
	runner.EXPECT().RunBufferedWithEnv(
		gomock.Any(), gomock.Any(), gomock.Any(), textVet, gomock.Any(),
	).Return(interfaces.CmdResult{}).Times(1)

	tr := newToolRunner(t, runner, toolOf(ctrl, textVet), cache)
	run := types.ToolRun{
		Name: textVet, Verb: textLint, Tool: textVet, Args: []string{"."},
		Each:   types.EachRun,
		Inputs: types.FileSelector{Exts: []string{textGo3}},
	}
	edited := run

	edited.Env = []string{"GOFLAGS=-tags=e2e"}

	cb := memoryCodebase(t, lintModules())

	for i, r := range []types.ToolRun{run, run, edited, edited} {
		res, err := tr.Run(t.Context(), cb, r, types.RunOptions{})
		require.NoError(t, err)

		want := []types.StepStatus{
			types.StatusPass, types.StatusSkip, types.StatusPass, types.StatusSkip,
		}[i]
		assert.Equal(t, want, res[0].Status, "run %d", i)
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
	runner.EXPECT().Exists(textShellcheck).Return(true).AnyTimes()

	tr := newToolRunner(t, runner, toolOf(ctrl, textShellcheck), nil)
	for _, each := range []string{types.EachTarget, types.EachRun} {
		res, err := tr.Run(t.Context(), memoryCodebase(t, map[string]string{textAMd: ""}),
			types.ToolRun{
				Name: textShellcheck, Verb: textLint, Tool: textShellcheck,
				Args:    []string{textTarget2},
				Targets: types.TargetSpec{Kind: textFiles, Glob: "**/*.sh"},
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
	runner.EXPECT().Exists(textBuf).Return(true).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), filepath.Join(toolrunRoot, "zarf", "proto"),
		textBuf, "breaking").Return(interfaces.CmdResult{
		ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout: []byte("removed field"),
	}).Times(timesArg2)

	tr := newToolRunner(t, runner, toolOf(ctrl, textBuf), cache)
	cb := memoryCodebase(
		t,
		map[string]string{"zarf/proto/a.proto": "syntax = \"proto3\";\n"},
	)
	run := types.ToolRun{
		Name: "breaking", Verb: textCheck, Tool: textBuf, Group: "Proto",
		Cwd: "zarf/proto", Args: []string{"breaking"}, Advisory: true,
		Inputs: types.FileSelector{Roots: []string{"zarf/proto"}},
	}

	for range iterations2 {
		res, err := tr.Run(t.Context(), cb, run, types.RunOptions{})
		require.NoError(t, err)
		require.Len(t, res, 1)
		assert.Equal(t, "Proto", res[0].Group)
		assert.Equal(t, types.StatusWarn, res[0].Status)
		assert.Equal(t, "removed field", res[0].Error)
	}

	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, textBuf, textLint).
		Return(interfaces.CmdResult{})

	res, err := tr.Run(t.Context(), cb, types.ToolRun{
		Name: textLint, Verb: textCheck, Tool: textBuf, Args: []string{textLint},
	}, types.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, textCheck, res[0].Group)

	_, err = tr.Run(t.Context(), cb, types.ToolRun{
		Name: textX, Verb: textCheck, Tool: textBuf, Cwd: "../elsewhere",
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
	runner.EXPECT().Exists(textTrivy).Return(true).AnyTimes()
	runner.EXPECT().Exists(textHadolint).Return(false).AnyTimes()
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, textTrivy, textImage, "api:1",
		"--exit-code", "1").Return(interfaces.CmdResult{})
	runner.EXPECT().RunBuffered(gomock.Any(), toolrunRoot, textTrivy, textImage, "api:2",
		"--exit-code", "1").Return(interfaces.CmdResult{})

	spec := toolOf(ctrl, textTrivy)
	tr, err := toolrun.NewFromConfig(toolrun.Config{}, toolrun.Deps{
		Decorations: fixtures.Decorations(), Runner: runner, Root: toolrunRoot,
		Cache: cache,
		Tools: func(name string) (interfaces.ToolSpec, bool) {
			if name == textHadolint {
				return toolOf(ctrl, textHadolint), true
			}

			return spec, name == textTrivy
		},
	})
	require.NoError(t, err)

	cb := memoryCodebase(t, map[string]string{textDockerfile: "FROM x\n"})

	res, err := tr.Run(t.Context(), cb,
		types.ToolRun{Name: textHadolint, Verb: textLint, Tool: textHadolint},
		types.RunOptions{Strict: true})
	require.NoError(t, err)
	assert.Equal(t, types.StatusFail, res[0].Status)

	image := types.ToolRun{
		Name: "trivy image", Verb: textSecurity, Tool: textTrivy,
		Args: []string{textImage, "{image}"}, FixArgs: []string{"--exit-code", "1"},
		Inputs: types.FileSelector{Paths: []string{textDockerfile}},
	}
	for _, img := range []string{"api:1", "api:2"} {
		res, err = tr.Run(t.Context(), cb, image, types.RunOptions{
			Fix: true, Vars: map[string]string{textImage: img},
		})
		require.NoError(t, err)
		assert.Equal(t, types.StatusPass, res[0].Status, img)
	}

	_, err = tr.Run(t.Context(), cb, image, types.RunOptions{})
	require.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	require.ErrorContains(t, err, "trivy image")
	assert.ErrorContains(t, err, textImage)
}

// TestServicesToolrun_Env tests a run's environment entries.
//
// Why this test is important:
//   - a tool that must not inherit a variable (a Python tool under a foreign
//     VIRTUAL_ENV) or needs one set per run is a row, not Go code.
//
// What it tests:
//   - Env entries expand placeholders and reach the runner's environment call; a run
//     with no Env keeps the plain call.
func TestServicesToolrun_Env(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	runner := mocks.NewMockCommandRunner(ctrl)
	runner.EXPECT().Exists(textRuff).Return(true)
	runner.EXPECT().RunBufferedWithEnv(gomock.Any(), gomock.Any(),
		[]string{textVirtualEnv, "MODE=ci"}, textRuff, textCheck).
		Return(interfaces.CmdResult{})

	tr := newToolRunner(t, runner, toolOf(ctrl, textRuff), nil)
	res, err := tr.Run(t.Context(), nil, types.ToolRun{
		Name: textRuff, Verb: textLint, Tool: textRuff, Args: []string{textCheck},
		Env: []string{textVirtualEnv, "MODE={mode}"},
	}, types.RunOptions{Vars: map[string]string{textMode: "ci"}})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPass, res[0].Status)
}
