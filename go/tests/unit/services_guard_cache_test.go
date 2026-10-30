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
)

// cachedGuardDeps returns guard deps over a dir cache under t's temp dir, a Go guard
// that counts its runs and fails the file named fail, and a two-module expander.
func cachedGuardDeps(
	t *testing.T,
	runs *atomic.Int32,
	fail string,
) guard.Deps {
	t.Helper()
	cache, err := stepcache.NewFromConfig(stepcache.Config{
		Kind: stepcache.KindDir, Dir: t.TempDir(),
	})
	require.NoError(t, err)
	return guard.Deps{
		Cache:    cache,
		Versions: func(tool string) string { return tool + "-1.0" },
		Expanders: map[string]guard.Expander{
			"modules": func(context.Context, interfaces.Codebase) (
				[]map[string]string, error,
			) {
				return []map[string]string{{"dir": "deploy"}, {"dir": "infra"}}, nil
			},
		},
		Go: map[string]guard.GoGuard{
			"validate": func(
				_ context.Context, _ interfaces.Codebase, _ *types.GuardRow,
				vars map[string]string,
			) ([]types.Finding, error) {
				runs.Add(1)
				if vars["dir"] == fail {
					return []types.Finding{{File: vars["dir"], Message: "bad"}}, nil
				}
				return nil, nil
			},
		},
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
	var runs atomic.Int32
	deps := cachedGuardDeps(t, &runs, "")
	row := types.GuardRow{
		Name: "validate", Verb: "test-infra", Kind: types.GuardGo, Each: "modules",
		Tool: "terraform", Inputs: types.FileSelector{Roots: []string{"{dir}"}},
	}
	gs, err := guard.NewFromConfig([]types.GuardRow{row}, deps)
	require.NoError(t, err)
	g := gs[0]
	files := guardFiles()

	v := g.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, v.Status)
	assert.Equal(t, int32(2), runs.Load())

	v = g.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusSkip, v.Status)
	assert.Equal(t, "cached", v.Detail)
	assert.Equal(t, int32(2), runs.Load(), "a cached pass runs nothing")

	files["infra/main.tf"] = "resource \"y\" {}\n"
	v = g.Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, v.Status)
	assert.Empty(t, v.Detail, "one expansion ran, so the row is not reported cached")
	assert.Equal(t, int32(3), runs.Load(), "only the edited module runs")

	// A failure is not cached: the failing module runs again, the passing one does not.
	var failing atomic.Int32
	fdeps := cachedGuardDeps(t, &failing, "infra")
	fg, err := guard.NewFromConfig([]types.GuardRow{row}, fdeps)
	require.NoError(t, err)
	for range 2 {
		v = fg[0].Run(t.Context(), memoryCodebase(t, files))
		assert.Equal(t, types.StatusFail, v.Status)
	}
	assert.Equal(t, int32(3), failing.Load())

	// No inputs: never cached.
	plain := row
	plain.Inputs = types.FileSelector{}
	pg, err := guard.NewFromConfig([]types.GuardRow{plain}, deps)
	require.NoError(t, err)
	before := runs.Load()
	pg[0].Run(t.Context(), memoryCodebase(t, files))
	pg[0].Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, before+4, runs.Load())

	// Another tool version misses.
	deps.Versions = func(string) string { return "2.0" }
	vg, err := guard.NewFromConfig([]types.GuardRow{row}, deps)
	require.NoError(t, err)
	before = runs.Load()
	v = vg[0].Run(t.Context(), memoryCodebase(t, files))
	assert.Equal(t, types.StatusPass, v.Status)
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
	deps := guard.Deps{
		Expanders: map[string]guard.Expander{
			"modules": func(context.Context, interfaces.Codebase) (
				[]map[string]string, error,
			) {
				return []map[string]string{{"dir": "a"}, {"dir": "b"}, {"dir": "c"}}, nil
			},
		},
		Go: map[string]guard.GoGuard{
			"slow": func(
				_ context.Context, _ interfaces.Codebase, _ *types.GuardRow,
				vars map[string]string,
			) ([]types.Finding, error) {
				n := inFlight.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(20 * time.Millisecond)
				inFlight.Add(-1)
				return []types.Finding{{File: vars["dir"], Message: "seen"}}, nil
			},
		},
	}
	gs, err := guard.NewFromConfig([]types.GuardRow{{
		Name: "slow", Verb: "check", Kind: types.GuardGo, Each: "modules",
	}}, deps)
	require.NoError(t, err)
	v := gs[0].Run(t.Context(), memoryCodebase(t, guardFiles()))
	assert.Equal(t, types.StatusFail, v.Status)
	require.Len(t, v.Findings, 3)
	assert.Equal(t, []string{"a", "b", "c"}, []string{
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
	row := types.GuardRow{
		Name: "validate", Verb: "test-infra", Kind: types.GuardGo, Each: "modules",
		Tool: "terraform", Inputs: types.FileSelector{Roots: []string{"{dir}"}},
		Data: map[string]any{"strict": false},
	}
	edited := row
	edited.Data = map[string]any{"strict": true}
	files := guardFiles()
	for i, r := range []types.GuardRow{row, row, edited, edited} {
		gs, err := guard.NewFromConfig([]types.GuardRow{r}, deps)
		require.NoError(t, err)
		gs[0].Run(t.Context(), memoryCodebase(t, files))
		assert.Equal(t, []int32{2, 2, 4, 4}[i], runs.Load(), "run %d", i)
	}
}
