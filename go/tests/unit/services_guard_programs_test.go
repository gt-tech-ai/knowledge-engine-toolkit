package unit_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/guard"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// programRows are expr rows (one with a for) whose verdicts over guardFiles are known.
func programRows() []types.GuardRow {
	deploy := fixtures.Build(func(row *types.FileSelector) {
		row.Globs = []string{textDeployGlob}
	})

	return []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "no-latest"
			row.Kind = types.GuardExpr
			row.Files = deploy
			row.Message = "latest"
			row.Expr = `files.all(p, !file[p].text.contains("app:latest"))`
		}),
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = "replicas-{n}"
			row.Kind = types.GuardExpr
			row.Files = deploy
			row.For = `[1, 2].map(i, {"n": i})`
			row.Expr = `docs.exists(d, string(d.doc.replicas) == vars.n)`
		}),
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

	want := runGuards(t, programRows(), new(guard.Deps))
	require.Equal(t, types.StatusFail, want["no-latest"].Status)
	assert.Equal(t, "latest", want["no-latest"].Findings[0].Message)
	require.Equal(
		t,
		types.StatusPass,
		want["replicas-{n}"].Status,
		want["replicas-{n}"].Detail,
	)

	progs := guard.NewPrograms()

	var group sync.WaitGroup

	got := make([]map[string]types.Verdict, makeInput)

	for i := range got {
		group.Go(
			func() {
				got[i] = runGuards(
					t,
					programRows(),
					fixtures.BuildRef(func(row *guard.Deps) {
						row.Programs = progs
					}),
				)
			},
		)
	}

	group.Wait()

	for _, vs := range got {
		assert.Equal(t, want, vs)
	}

	bad := []types.GuardRow{
		fixtures.Build(func(row *types.GuardRow) {
			row.Name = textBad
			row.Kind = types.GuardExpr
			row.Expr = "files.("
		}),
	}
	for range iterations {
		_, err := guard.NewFromConfig(
			bad,
			fixtures.BuildRef(func(row *guard.Deps) {
				row.Programs = progs
			}),
		)
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
			_, err := guard.NewFromConfig(rows, new(guard.Deps))
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	benchmarkNewFromConfigProgramsSharedAndFresh(b, rows)
}

// benchmarkNewFromConfigProgramsSharedAndFresh continues
// BenchmarkNewFromConfig_Programs: shared and fresh.
func benchmarkNewFromConfigProgramsSharedAndFresh(b *testing.B, rows []types.GuardRow) {
	b.Helper()

	b.Run(textShared, func(b *testing.B) {
		progs := guard.NewPrograms()

		for b.Loop() {
			_, err := guard.NewFromConfig(
				rows,
				fixtures.BuildRef(func(row *guard.Deps) {
					row.Programs = progs
				}),
			)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
