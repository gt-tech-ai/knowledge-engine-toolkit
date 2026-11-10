package unit_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/report"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/stdx"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/tests/fixtures"
)

// consoleGolden reads one console golden from testdata/report. The goldens were
// captured from the console this package replaces, so they pin its exact bytes.
func consoleGolden(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(
		filepath.Clean(
			filepath.Join(textDotDot, textDotDot, textDotDot, "testdata", "report", name),
		),
	)
	require.NoError(t, err)

	return string(data)
}

// consoleHints is the remediation hint the fixed run's ruff failure matches.
func consoleHints() []report.Hint {
	return []report.Hint{
		{
			Group:  textLintTitle,
			Substr: "golangci",
			Text:   "Run: search lint --go --fix  (to auto-fix)",
		},
		{
			Group:  textLintTitle,
			Substr: textRuff,
			Text:   "Run: search lint --python --fix  (to auto-fix)",
		},
	}
}

// consoleRun is the fixed gate run the goldens render: two phases, a pass, a failure
// with noisy output, a skip, a warning, and a step with children and a test count.
func consoleRun() (lint, unit types.StepResults) {
	noisy := []byte("=== RUN   TestX\n--- PASS: TestX (0.00s)\n{\"level\":\"info\"}\n" +
		"apps/python/server/x.py:12:5: F401 `os` imported but unused\n" +
		"apps/python/server/y.py:3:1: E501 line too long\nFound 2 errors.\n")

	lint = types.StepResults{
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "golangci (Go)"
			row.Group = textLintTitle
			row.Status = types.StatusPass
			row.Duration = durationRun * time.Millisecond
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "ruff (Python)"
			row.Group = textLintTitle
			row.Status = types.StatusFail
			row.Duration = durationConsoleRun * time.Millisecond
			row.Error = "INTERNAL: ruff found 2 issues"
			row.Detail = noisy
		}),
	}
	unit = types.StepResults{
		{
			Name:     "Go",
			Group:    textUnitTests,
			Status:   types.StatusPass,
			Duration: consoleRunSeconds * time.Second,
			Tests:    tests,
			Children: types.StepResults{
				fixtures.Build(func(row *types.StepResult) {
					row.Name = "Go/apps/go/cli"
					row.Status = types.StatusPass
					row.Duration = consoleStepSeconds * time.Second
					row.Tests = testsRun
				}),
				fixtures.Build(func(row *types.StepResult) {
					row.Name = "Go/pkg/go"
					row.Status = types.StatusPass
					row.Duration = time.Second
					row.Tests = testsConsoleRun
				}),
			},
			Error:  "",
			Detail: nil,
			Output: false,
		},
		fixtures.Build(func(row *types.StepResult) {
			row.Name = "TypeScript"
			row.Group = textUnitTests
			row.Status = types.StatusSkip
		}),
		fixtures.Build(func(row *types.StepResult) {
			row.Name = textDocsLower
			row.Group = textUnitTests
			row.Status = types.StatusWarn
			row.Error = "2 stale links"
		}),
	}

	return lint, unit
}

// replayConsoleRun drives obs through the fixed run.
func replayConsoleRun(obs interfaces.ExecutionObserver) {
	lint, unit := consoleRun()

	obs.OnGateStart(textPreflight, []string{textLintTitle, textUnitTests})

	for _, phase := range []struct {
		name    string
		results types.StepResults
		elapsed time.Duration
	}{{textLintTitle, lint, seconds * time.Second}, {
		textUnitTests, unit,
		secondsRun * time.Second,
	}} {
		names := make([]string, 0, len(phase.results))
		for _, r := range phase.results {
			names = append(names, r.Name)
		}

		obs.OnPhaseStart(phase.name, names)

		for _, r := range phase.results {
			obs.OnStepStart(r.Name, r.Group)
			obs.OnStepComplete(&r)
		}

		obs.OnPhaseComplete(phase.name, phase.results, phase.elapsed)
	}

	all := append(append(types.StepResults{}, lint...), unit...)
	obs.OnGateComplete(textPreflight, all, 6*time.Second)
}

// newTestConsole returns a console at level over a fresh error buffer.
func newTestConsole(
	t *testing.T,
	cfg report.ConsoleConfig,
	level report.Level,
) (*report.Console, *bytes.Buffer) {
	t.Helper()

	var out, errOut bytes.Buffer

	cfg.Hints = consoleHints()

	c, err := report.NewConsole(types.Streams{Out: &out, Err: &errOut}, cfg)
	require.NoError(t, err)
	c.SetLevel(level)

	return c, &errOut
}

// TestConsole_Golden tests that the console renders the fixed run, coded errors, the
// command hook and the logger byte for byte as the goldens record.
//
// Why this test is important:
//   - CLI operators and CI logs read this output; a CLI that moves onto the console
//     must print exactly what it printed before, including the ANSI sequences.
//
// What it tests:
//   - Observer() at each level and mode (CI line with annotations, TTY line with and
//     without color, quiet, the verbose table), FormatError at normal and verbose
//     levels, and the verbose hook and logger each write exactly their golden.
func TestConsole_Golden(t *testing.T) {
	t.Parallel()

	failure := apperr.Wrap(
		apperr.Sentinel("dial tcp: i/o timeout"),
		apperr.CodeUpstream,
		"registry unreachable",
	)
	observe := func(c *report.Console) { replayConsoleRun(c.Observer()) }

	cases := testConsoleGoldenTable(t, failure, observe)

	for _, testCase := range cases {
		t.Run(testCase.golden, func(t *testing.T) {
			t.Parallel()

			c, errOut := newTestConsole(t, testCase.cfg, testCase.level)
			testCase.run(c)
			assert.Equal(t, consoleGolden(t, testCase.golden), errOut.String())
		})
	}
}

// testConsoleGoldenRow is one row of testConsoleGoldenTable.
type testConsoleGoldenRow struct {
	run    func(c *report.Console)
	golden string
	cfg    report.ConsoleConfig
	level  report.Level
}

// testConsoleGoldenTable builds the table TestConsole_Golden runs.
func testConsoleGoldenTable(
	t *testing.T,
	failure error,
	observe func(c *report.Console),
) []testConsoleGoldenRow {
	t.Helper()

	return []testConsoleGoldenRow{
		{
			observe,
			"line_ci.txt",
			fixtures.Build(func(row *report.ConsoleConfig) {
				row.CI = true
			}),
			report.LevelNormal,
		},
		{observe, "line_tty.txt", stdx.Zero[report.ConsoleConfig](), report.LevelNormal},
		{
			observe,
			"line_tty_color.txt",
			fixtures.Build(func(row *report.ConsoleConfig) {
				row.Color = true
			}),
			report.LevelNormal,
		},
		{observe, "line_quiet.txt", stdx.Zero[report.ConsoleConfig](), report.LevelQuiet},
		{
			observe,
			"table_verbose.txt",
			stdx.Zero[report.ConsoleConfig](),
			report.LevelVerbose,
		},
		{func(c *report.Console) {
			assert.Equal(t, 1, c.FormatError(failure))
		}, "format_error.txt", stdx.Zero[report.ConsoleConfig](), report.LevelNormal},
		{func(c *report.Console) {
			c.FormatError(failure)
		}, "format_error_verbose.txt", stdx.Zero[report.ConsoleConfig](), report.LevelVerbose},
		{func(console *report.Console) {
			hook := console.CommandHook()
			hook.OnStart("go vet ./...")
			hook.OnSuccess("go vet ./...")
			hook.OnFailure("ruff check .")

			log := console.Logger().With("module", "cli")
			log.Debug(textResolved, textTools, debugInput)
			log.Info("installing")
			log.Warn("slow mirror")
			log.Error("install failed", textTool, textRuff)
		}, "hook_logger_verbose.txt", fixtures.Build(func(row *report.ConsoleConfig) {
			row.Color = true
		}), report.LevelVerbose},
	}
}

// TestConsole_LevelsAndCodes tests the console's level filtering, its exit codes and
// its handling of an uncoded error.
//
// Why this test is important:
//   - --quiet must still surface errors, a quiet hook must stay silent, and the exit
//     code is how scripts and CI tell a usage error from an outage.
//
// What it tests:
//   - At LevelQuiet Info, Successf, Warnf, Infof and the hook print nothing while Error
//     and Errorf print; at LevelNormal Successf and Warnf print; ExitCode maps
//     invalid input, not found and quality failed to 2, unavailable to 3 and everything
//     else to 1; an uncoded error prints "Error: <err>" and exits 1; missing streams
//     are CodeInvalidInput.
func TestConsole_LevelsAndCodes(t *testing.T) {
	t.Parallel()

	console, errOut := newTestConsole(
		t,
		stdx.Zero[report.ConsoleConfig](),
		report.LevelQuiet,
	)
	console.Logger().Info(textHidden)
	console.CommandHook().OnStart(textHidden)
	console.Successf(textHidden)
	console.Warnf(textHidden)
	console.Infof(textHidden)
	console.Logger().Error("shown")
	console.Errorf("also %s", "shown")
	assert.Equal(t, "shown\nalso shown\n", errOut.String())

	console.SetLevel(report.LevelNormal)
	errOut.Reset()
	console.Successf("done %d", 1)
	console.Warnf("careful")
	assert.Equal(t, "done 1\ncareful\n", errOut.String())
	console.SetLevel(report.LevelQuiet)
	assert.Equal(t, report.LevelQuiet, console.Level())

	errOut.Reset()
	assert.Equal(t, 1, console.FormatError(apperr.Sentinel(textBoom)))
	assert.Equal(t, "Error: boom\n", errOut.String())

	for code, want := range map[apperr.ErrorCode]int{
		apperr.CodeInvalidInput: codeInvalidInput, apperr.CodeNotFound: codeNotFound,
		apperr.CodeQualityFailed: codeQualityFailed,
		apperr.CodeUnavailable:   codeUnavailable, apperr.CodeUpstream: 1,
		apperr.CodeInternal: 1,
	} {
		assert.Equal(t, want, report.ExitCode(code), code)
	}

	_, err := report.NewConsole(
		stdx.Zero[types.Streams](),
		stdx.Zero[report.ConsoleConfig](),
	)
	assert.True(t, apperr.Is(err, apperr.CodeInvalidInput))
}

// TestConsole_StepSummary tests the GitHub step summary, written directly and appended
// by the console's observer.
//
// Why this test is important:
//   - The step summary is a workflow run's verdict page; it must match what the CLI
//     wrote before, and an all-pass run collapses to one line.
//
// What it tests:
//   - WriteStepSummary over the fixed run equals the golden; an all-pass run is the
//     one-line form; with a summary path set, Observer() appends the golden to the file.
func TestConsole_StepSummary(t *testing.T) {
	t.Parallel()

	lint, unit := consoleRun()

	var buf bytes.Buffer

	require.NoError(
		t,
		report.WriteStepSummary(&buf, append(lint, unit...), 6*time.Second),
	)
	assert.Equal(t, consoleGolden(t, "step_summary.md"), buf.String())

	buf.Reset()
	require.NoError(t, report.WriteStepSummary(&buf, lint[:1], time.Second))
	assert.Equal(t, "&#x2705; **Lint**: All 1 checks passed (1s)\n\n", buf.String())

	path := filepath.Join(t.TempDir(), "summary.md")
	console, _ := newTestConsole(
		t,
		fixtures.Build(func(row *report.ConsoleConfig) {
			row.CI = true
			row.StepSummaryPath = path
		}),
		report.LevelNormal,
	)
	replayConsoleRun(console.Observer())

	got, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)
	assert.Equal(t, consoleGolden(t, "step_summary.md"), string(got))
}

// TestConsole_Groups tests the console's log sections.
//
// Why this test is important:
//   - A long step's output is folded into a GitHub Actions group so a CI log stays
//     readable; outside GitHub Actions a stray "::group::" line would be noise, and a
//     normal run's phase lines already title the section.
//
// What it tests:
//   - Under GitHubActions, Groupf and EndGroup write the group markers at any level.
//   - Otherwise Groupf writes a header only when verbose, and EndGroup writes nothing.
func TestConsole_Groups(t *testing.T) {
	t.Parallel()

	gha, out := newTestConsole(
		t,
		fixtures.Build(func(row *report.ConsoleConfig) {
			row.GitHubActions = true
		}),
		report.LevelQuiet,
	)
	gha.Groupf("Unit %s", "Tests")
	gha.EndGroup()
	assert.Equal(t, "::group::Unit Tests\n::endgroup::\n", out.String())

	plain, out := newTestConsole(t, stdx.Zero[report.ConsoleConfig](), report.LevelNormal)
	plain.Groupf(textUnitTests)
	plain.EndGroup()
	assert.Empty(t, out.String())

	plain.SetLevel(report.LevelVerbose)
	plain.Groupf(textUnitTests)
	assert.Equal(t, "\n-- Unit Tests --\n\n", out.String())
}

// TestFoundationReport_ConsoleConcurrentWrites tests that a console is safe to write
// from several goroutines.
//
// Why this test is important:
//   - a procedure's probes and a suite's fan-out log through one console at once; an
//     unguarded writer races (a data race under -race on a buffer) and interleaves
//     half-written lines on a terminal.
//
// What it tests:
//   - 50 goroutines each printing 20 lines through one console leave 1,000 whole lines,
//     with no race reported.
func TestFoundationReport_ConsoleConcurrentWrites(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	console, err := report.NewConsole(
		types.Streams{Out: &out, Err: &errOut},
		fixtures.Build(func(row *report.ConsoleConfig) {
			row.CI = true
		}),
	)
	require.NoError(t, err)

	var group sync.WaitGroup

	for range iterationsWrites {
		group.Go(func() {
			for range 20 {
				console.Infof("line %s", textX)
			}
		})
	}

	group.Wait()

	lines := strings.Split(strings.TrimSuffix(errOut.String(), textNewline), textNewline)
	require.Len(t, lines, wantLinesLen)

	for _, l := range lines {
		assert.Equal(t, "line x", l)
	}
}

const (
	// codeInvalidInput is the value for apperr.CodeInvalidInput in
	// TestConsole_LevelsAndCodes.
	codeInvalidInput = 2

	// codeNotFound is the value for apperr.CodeNotFound in TestConsole_LevelsAndCodes.
	codeNotFound = 2

	// codeQualityFailed is the value for apperr.CodeQualityFailed in
	// TestConsole_LevelsAndCodes.
	codeQualityFailed = 2

	// codeUnavailable is the value for apperr.CodeUnavailable in
	// TestConsole_LevelsAndCodes.
	codeUnavailable = 3

	// debugInput is the keysAndValues passed to Debug in testConsoleGoldenTable.
	debugInput = 3

	// durationConsoleRun is the factor multiplying time.Millisecond in consoleRun.
	durationConsoleRun = 800

	// consoleRunSeconds is the factor multiplying time.Second in consoleRun.
	consoleRunSeconds = 4

	// consoleStepSeconds is the factor multiplying time.Second in consoleRun.
	consoleStepSeconds = 3

	// durationRun is the factor multiplying time.Millisecond in consoleRun.
	durationRun = 1200

	// iterationsWrites is the iteration count in
	// TestFoundationReport_ConsoleConcurrentWrites.
	iterationsWrites = 50

	// secondsRun is the factor multiplying time.Second in replayConsoleRun.
	secondsRun = 4

	// tests is the Tests field in consoleRun.
	tests = 42

	// testsConsoleRun is the value assigned to row.Tests in consoleRun.
	testsConsoleRun = 12

	// testsRun is the value assigned to row.Tests in consoleRun.
	testsRun = 30

	// wantLinesLen is the expected lines len.
	wantLinesLen = 1000
)

const (
	// textHidden is the literal "hidden".
	textHidden = "hidden"
)
