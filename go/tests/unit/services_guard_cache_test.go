package unit_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/stepcache"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// cachedGuardDeps returns guard deps over a dir cache under t's temp dir, a Go guard
// that counts its runs and fails the file named fail, and a two-module expander.
func cachedGuardDeps(
	t *testing.T,
	runs *atomic.Int32,
	fail string,
) guard.Deps {
	t.Helper()

	cache, err := stepcache.NewFromConfig(fixtures.BuildRef(func(row *stepcache.Config) {
		row.Kind = stepcache.KindDir
		row.Dir = t.TempDir()
	}))
	require.NoError(t, err)

	return fixtures.Build(func(row *guard.Deps) {
		row.Cache = cache
		row.Versions = func(tool string) string { return tool + "-1.0" }
		row.Expanders = map[string]guard.Expander{textModules: twoModuleExpander}
		row.Go = map[string]guard.GoGuard{textValidate: countingGoGuard(runs, fail)}
	})
}

// twoModuleExpander expands a guard over the deploy and infra modules.
func twoModuleExpander(
	context.Context,
	interfaces.Codebase,
) ([]map[string]string, error) {
	return []map[string]string{{textDir: textDeploy}, {textDir: textInfra}}, nil
}

// countingGoGuard counts its runs and reports one finding for the directory named fail.
func countingGoGuard(runs *atomic.Int32, fail string) guard.GoGuard {
	return func(
		_ context.Context, _ interfaces.Codebase, _ *types.GuardRow,
		vars map[string]string,
	) ([]types.Finding, error) {
		runs.Add(1)

		if vars[textDir] != fail {
			return []types.Finding{}, nil
		}

		return []types.Finding{
			fixtures.Build(func(row *types.Finding) {
				row.File = vars[textDir]
				row.Message = textBad
			}),
		}, nil
	}
}

// TestServicesGuard_GoCachedOnInputs tests a Go row's pass cached on its inputs.
//
// Why this test is important:
//   - a guard that shells a tool over every module (terraform validate) costs tens of
//     seconds a run; preflight on an unchanged tree must skip it, reported as cached,
//     and a changed module or a different tool version must run it again. A failure is
//     never cached.
//
// What it tests:
//   - the first run runs every expansion; the second, on the same content, runs none and
//     reports Skip "cached"; editing one module's file re-runs that module only; a
//     failing expansion is run again on the next run while the passing one stays
//     cached; a row without inputs is never cached; a different tool version misses.
func TestServicesGuard_GoCachedOnInputs(t *testing.T) {
	t.Parallel()

	testServicesGuardGoCachedOnInputsChecksRun(t)
}

// testServicesGuardGoCachedOnInputsChecksRun continues
// testServicesGuardGoCachedOnInputsFailuresRerun: checks run.
func testServicesGuardGoCachedOnInputsChecksRun(t *testing.T) {
	t.Helper()

	var runs atomic.Int32

	deps := cachedGuardDeps(t, &runs, "")
	row := fixtures.Build(func(row *types.GuardRow) {
		row.Name = textValidate
		row.Verb = "test-infra"
		row.Kind = types.GuardGo
		row.Each = textModules
		row.Tool = textTerraform
		row.Inputs = fixtures.Build(func(row *types.FileSelector) {
			row.Roots = []string{"{dir}"}
		})
	})
	gs, err := guard.NewFromConfig([]types.GuardRow{row}, &deps)
	require.NoError(t, err)

	g := gs[0]

	testServicesGuardGoCachedOnInputsCachesAndSkips(t, &runs, &deps, &row, g)
}

// testServicesGuardGoCachedOnInputsCachesAndSkips continues
// testServicesGuardGoCachedOnInputs: caches and skips.
func testServicesGuardGoCachedOnInputsCachesAndSkips(
	t *testing.T,
	runs *atomic.Int32,
	deps *guard.Deps,
	row *types.GuardRow,
	currentGuard interfaces.Guard,
) {
	t.Helper()

	files := guardFiles()

	verdict := currentGuard.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, verdict.Status)
	assert.Equal(t, int32(wantRunsSkips), runs.Load())

	verdict = currentGuard.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusSkip, verdict.Status)
	assert.Equal(t, textCached, verdict.Detail)
	assert.Equal(t, int32(wantRunsSkips), runs.Load(), "a cached pass runs nothing")

	files[textInfraMainTf] = "resource \"y\" {}\n"
	verdict = currentGuard.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, verdict.Status)
	assert.Empty(
		t,
		verdict.Detail,
		"one expansion ran, so the *row is not reported cached",
	)
	assert.Equal(t, int32(wantRuns), runs.Load(), "only the edited module runs")

	// A failure is not cached: the failing module runs again, the passing one does not.
	var failing atomic.Int32

	testServicesGuardGoCachedOnInputsFailuresRerun(
		t,
		runs,
		deps,
		row,
		files,
		verdict,
		&failing,
	)
}

// testServicesGuardGoCachedOnInputsFailuresRerun continues
// testServicesGuardGoCachedOnInputsCachesAndSkips: failures rerun.
func testServicesGuardGoCachedOnInputsFailuresRerun(
	t *testing.T,
	runs *atomic.Int32,
	depsRef *guard.Deps,
	row *types.GuardRow,
	files map[string]string,
	verdict types.Verdict,
	failing *atomic.Int32,
) {
	t.Helper()

	deps := *depsRef

	var err error

	fdeps := cachedGuardDeps(t, failing, textInfra)
	fg, err := guard.NewFromConfig([]types.GuardRow{*row}, &fdeps)
	require.NoError(t, err)

	for range iterations {
		verdict = fg[0].Run(t.Context(), memoryCodebase(t, files))
		assert.Equal(t, types.StatusFail, verdict.Status)
	}

	assert.Equal(t, int32(wantFailing), failing.Load())

	// No inputs: never cached.
	plain := *row

	plain.Inputs = stdx.Zero[types.FileSelector]()

	guards, err := guard.NewFromConfig([]types.GuardRow{plain}, &deps)
	require.NoError(t, err)

	before := runs.Load()

	guards[0].Run(t.Context(), memoryCodebase(t, files))
	guards[0].Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, before+4, runs.Load())

	// Another tool version misses.
	deps.Versions = func(string) string { return "2.0" }

	vg, err := guard.NewFromConfig([]types.GuardRow{*row}, &deps)
	require.NoError(t, err)

	before = runs.Load()
	verdict = vg[0].Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, verdict.Status)
	assert.Equal(t, before+2, runs.Load())
}

// TestServicesGuard_ExpansionsRunInParallel tests that a row's expansions fan out.
//
// Why this test is important:
//   - a row expanded over every module or test file (terraform validate, promtool) is
//     only as fast as its slowest expansion when they run together; run one at a time
//     they are the sum.
//
// What it tests:
//   - two expansions of a Go row are in flight at once, and their findings come back in
//     expansion order.
func TestServicesGuard_ExpansionsRunInParallel(t *testing.T) {
	t.Parallel()

	var inFlight, peak atomic.Int32

	deps := fixtures.Build(func(row *guard.Deps) {
		row.Expanders = map[string]guard.Expander{
			textModules: func(context.Context, interfaces.Codebase) (
				[]map[string]string, error,
			) {
				return []map[string]string{
					{textDir: textA},
					{textDir: textB},
					{textDir: textC},
				}, nil
			},
		}
		row.Go = map[string]guard.GoGuard{
			textSlow: func(
				_ context.Context, _ interfaces.Codebase, _ *types.GuardRow,
				vars map[string]string,
			) ([]types.Finding, error) {
				n := inFlight.Add(1)
				recordPeak(&peak, n)

				time.Sleep(20 * time.Millisecond)
				inFlight.Add(-1)

				return []types.Finding{
					fixtures.Build(func(row *types.Finding) {
						row.File = vars[textDir]
						row.Message = "seen"
					}),
				}, nil
			},
		}
	})
	guards, err := guard.NewFromConfig(
		[]types.GuardRow{fixtures.Build(func(row *types.GuardRow) {
			row.Name = textSlow
			row.Verb = textCheck
			row.Kind = types.GuardGo
			row.Each = textModules
		})},
		&deps,
	)
	require.NoError(t, err)

	v := guards[0].Run(t.Context(), memoryCodebase(t, guardFiles()))
	assert.Equal(t, types.StatusFail, v.Status)
	require.Len(t, v.Findings, wantFindingsLen)
	assert.Equal(t, []string{textA, textB, textC}, []string{
		v.Findings[0].File, v.Findings[1].File, v.Findings[2].File,
	})
	assert.Greater(t, peak.Load(), int32(1), "expansions run concurrently")
}

// TestServicesGuard_RowEditMissesCache tests that a Go row's cache key covers the row's
// whole definition.
//
// Why this test is important:
//   - a row's rule lives in configuration beside its inputs, not in them; a key over the
//     name and inputs alone replays a pass recorded under the old rule after the rule
//     is edited, so the edit is never checked.
//
// What it tests:
//   - after a cached pass, the same row with different data runs every expansion again,
//     and the edited row's own pass is then cached.
func TestServicesGuard_RowEditMissesCache(t *testing.T) {
	t.Parallel()

	var runs atomic.Int32

	deps := cachedGuardDeps(t, &runs, "")
	row := fixtures.Build(func(row *types.GuardRow) {
		row.Name = textValidate
		row.Verb = "test-infra"
		row.Kind = types.GuardGo
		row.Each = textModules
		row.Tool = textTerraform
		row.Inputs = fixtures.Build(func(row *types.FileSelector) {
			row.Roots = []string{"{dir}"}
		})
		row.Data = map[string]any{"strict": false}
	})
	edited := row

	edited.Data = map[string]any{"strict": true}

	files := guardFiles()

	for i, r := range []types.GuardRow{row, row, edited, edited} {
		gs, err := guard.NewFromConfig([]types.GuardRow{r}, &deps)
		require.NoError(t, err)
		gs[0].Run(t.Context(), memoryCodebase(t, files))
		assert.Equal(t, []int32{2, 2, 4, 4}[i], runs.Load(), "run %d", i)
	}
}

const (
	// wantFailing is the expected failing.
	wantFailing = 3

	// wantFindingsLen is the expected findings len.
	wantFindingsLen = 3

	// wantRunsSkips is the expected runs skips.
	wantRunsSkips = 2
)

const (
	// textModules is the literal "modules".
	textModules = "modules"
)
