package unit_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
)

// programRows are expr rows (one with a for) whose verdicts over guardFiles are known.
func programRows() []types.GuardRow {
	deploy := types.FileSelector{Globs: []string{textDeploy2}}

	return []types.GuardRow{
		{
			Name: "no-latest", Kind: types.GuardExpr, Files: deploy, Message: "latest",
			Expr: `files.all(p, !file[p].text.contains("app:latest"))`,
		},
		{
			Name: "replicas-{n}", Kind: types.GuardExpr, Files: deploy,
			For:  `[1, 2].map(i, {"n": i})`,
			Expr: `docs.exists(d, string(d.doc.replicas) == vars.n)`,
		},
	}
}

// TestServicesGuard_SharedPrograms tests guards built through one shared Programs.
//
// Why this test is important:
//   - a caller that builds guards repeatedly (one gate per phase or per run) shares one
//     Programs so each expression type-checks once; a cached program must evaluate
//     exactly as a fresh one, across concurrent builds, and a broken expression must
//     fail every build rather than be remembered as compiled.
//
// What it tests:
//   - two concurrent NewFromConfig calls over one Programs give the verdicts a build
//     without one gives (an expr row failing with its message, a for row's expansions
//     passing); a CEL syntax error is CodeInvalidInput on each of two builds.
func TestServicesGuard_SharedPrograms(t *testing.T) {
	t.Parallel()

	want := runGuards(t, programRows(), &guard.Deps{})
	require.Equal(t, types.StatusFail, want["no-latest"].Status)
	assert.Equal(t, "latest", want["no-latest"].Findings[0].Message)
	require.Equal(
		t,
		types.StatusPass,
		want["replicas-{n}"].Status,
		want["replicas-{n}"].Detail,
	)

	progs := guard.NewPrograms()

	var wg sync.WaitGroup

	got := make([]map[string]types.Verdict, size2)

	for i := range got {
		wg.Go(
			func() { got[i] = runGuards(t, programRows(), &guard.Deps{Programs: progs}) },
		)
	}

	wg.Wait()

	for _, vs := range got {
		assert.Equal(t, want, vs)
	}

	bad := []types.GuardRow{{Name: textBad, Kind: types.GuardExpr, Expr: "files.("}}
	for range iterations2 {
		_, err := guard.NewFromConfig(bad, &guard.Deps{Programs: progs})
		require.Error(t, err)
		assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
	}
}

// BenchmarkNewFromConfig_Programs measures building the expr rows' guards with a fresh
// Programs per build (nil) against one shared across builds, the case a caller that
// builds a gate repeatedly hits.
func BenchmarkNewFromConfig_Programs(b *testing.B) {
	rows := programRows()

	b.Run("fresh", func(b *testing.B) {
		for b.Loop() {
			_, err := guard.NewFromConfig(rows, &guard.Deps{})
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	benchmarkNewFromConfigProgramsRest(b, rows)
}

// benchmarkNewFromConfigProgramsRest continues BenchmarkNewFromConfig_Programs with
// its remaining steps.
func benchmarkNewFromConfigProgramsRest(b *testing.B, rows []types.GuardRow) {
	b.Helper()

	b.Run(textShared, func(b *testing.B) {
		progs := guard.NewPrograms()
		for b.Loop() {
			_, err := guard.NewFromConfig(
				rows,
				&guard.Deps{Programs: progs},
			)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
