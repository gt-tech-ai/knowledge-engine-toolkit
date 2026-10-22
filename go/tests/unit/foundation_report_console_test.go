package unit_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/report"
)

// consoleGolden reads one console golden from testdata/report. The goldens were
// captured from the console this package replaces, so they pin its exact bytes.
func consoleGolden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "report", name))
	require.NoError(t, err)
	return string(data)
}

// consoleHints is the remediation hint the fixed run's ruff failure matches.
func consoleHints() []report.Hint {
	return []report.Hint{
		{
			Group:  "Lint",
			Substr: "golangci",
			Text:   "Run: search lint --go --fix  (to auto-fix)",
		},
		{
			Group:  "Lint",
			Substr: "ruff",
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
		{
			Name:     "golangci (Go)",
			Group:    "Lint",
			Status:   types.StatusPass,
			Duration: 1200 * time.Millisecond,
		},
		{
			Name:     "ruff (Python)",
			Group:    "Lint",
			Status:   types.StatusFail,
			Duration: 800 * time.Millisecond,
			Error:    "INTERNAL: ruff found 2 issues",
			Detail:   noisy,
		},
	}
	unit = types.StepResults{
		{
			Name:     "Go",
			Group:    "Unit Tests",
			Status:   types.StatusPass,
			Duration: 4 * time.Second,
			Tests:    42,
			Children: types.StepResults{
				{
					Name:     "Go/apps/go/cli",
					Status:   types.StatusPass,
					Duration: 3 * time.Second,
					Tests:    30,
				},
				{
					Name:     "Go/pkg/go",
					Status:   types.StatusPass,
					Duration: time.Second,
					Tests:    12,
				},
			},
		},
		{Name: "TypeScript", Group: "Unit Tests", Status: types.StatusSkip},
		{
			Name:   "docs",
			Group:  "Unit Tests",
			Status: types.StatusWarn,
			Error:  "2 stale links",
		},
	}
	return lint, unit
}

// replayConsoleRun drives obs through the fixed run.
func replayConsoleRun(obs interfaces.ExecutionObserver) {
	lint, unit := consoleRun()
	obs.OnGateStart("preflight", []string{"Lint", "Unit Tests"})
	for _, phase := range []struct {
		name    string
		results types.StepResults
		elapsed time.Duration
	}{{"Lint", lint, 2 * time.Second}, {"Unit Tests", unit, 4 * time.Second}} {
		names := make([]string, 0, len(phase.results))
		for _, r := range phase.results {
			names = append(names, r.Name)
		}
		obs.OnPhaseStart(phase.name, names)
		for _, r := range phase.results {
			obs.OnStepStart(r.Name, r.Group)
			obs.OnStepComplete(r)
		}
		obs.OnPhaseComplete(phase.name, phase.results, phase.elapsed)
	}
	all := append(append(types.StepResults{}, lint...), unit...)
	obs.OnGateComplete("preflight", all, 6*time.Second)
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
	cases := []struct { //nolint:govet // fieldalignment: rows read in field order
		golden string
		cfg    report.ConsoleConfig
		level  report.Level
		run    func(c *report.Console)
	}{
		{"line_ci.txt", report.ConsoleConfig{CI: true}, report.LevelNormal, observe},
		{"line_tty.txt", report.ConsoleConfig{}, report.LevelNormal, observe},
		{
			"line_tty_color.txt",
			report.ConsoleConfig{Color: true},
			report.LevelNormal,
			observe,
		},
		{"line_quiet.txt", report.ConsoleConfig{}, report.LevelQuiet, observe},
		{"table_verbose.txt", report.ConsoleConfig{}, report.LevelVerbose, observe},
		{
			"format_error.txt",
			report.ConsoleConfig{},
			report.LevelNormal,
			func(c *report.Console) {
				assert.Equal(t, 1, c.FormatError(failure))
			},
		},
		{
			"format_error_verbose.txt",
			report.ConsoleConfig{},
			report.LevelVerbose,
			func(c *report.Console) {
				c.FormatError(failure)
			},
		},
		{
			"hook_logger_verbose.txt",
			report.ConsoleConfig{Color: true},
			report.LevelVerbose,
			func(c *report.Console) {
				hook := c.CommandHook()
				hook.OnStart("go vet ./...")
				hook.OnSuccess("go vet ./...")
				hook.OnFailure("ruff check .")
				log := c.Logger().With("module", "cli")
				log.Debug("resolved", "tools", 3)
				log.Info("installing")
				log.Warn("slow mirror")
				log.Error("install failed", "tool", "ruff")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			c, errOut := newTestConsole(t, tc.cfg, tc.level)
			tc.run(c)
			assert.Equal(t, consoleGolden(t, tc.golden), errOut.String())
		})
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

	c, errOut := newTestConsole(t, report.ConsoleConfig{}, report.LevelQuiet)
	c.Logger().Info("hidden")
	c.CommandHook().OnStart("hidden")
	c.Successf("hidden")
	c.Warnf("hidden")
	c.Infof("hidden")
	c.Logger().Error("shown")
	c.Errorf("also %s", "shown")
	assert.Equal(t, "shown\nalso shown\n", errOut.String())

	c.SetLevel(report.LevelNormal)
	errOut.Reset()
	c.Successf("done %d", 1)
	c.Warnf("careful")
	assert.Equal(t, "done 1\ncareful\n", errOut.String())
	c.SetLevel(report.LevelQuiet)
	assert.Equal(t, report.LevelQuiet, c.Level())

	errOut.Reset()
	assert.Equal(t, 1, c.FormatError(apperr.Sentinel("boom")))
	assert.Equal(t, "Error: boom\n", errOut.String())

	for code, want := range map[apperr.ErrorCode]int{
		apperr.CodeInvalidInput: 2, apperr.CodeNotFound: 2, apperr.CodeQualityFailed: 2,
		apperr.CodeUnavailable: 3, apperr.CodeUpstream: 1, apperr.CodeInternal: 1,
	} {
		assert.Equal(t, want, report.ExitCode(code), code)
	}

	_, err := report.NewConsole(types.Streams{}, report.ConsoleConfig{})
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
	c, _ := newTestConsole(
		t,
		report.ConsoleConfig{CI: true, StepSummaryPath: path},
		report.LevelNormal,
	)
	replayConsoleRun(c.Observer())
	got, err := os.ReadFile(path)
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

	gha, out := newTestConsole(t, report.ConsoleConfig{GitHubActions: true}, report.LevelQuiet)
	gha.Groupf("Unit %s", "Tests")
	gha.EndGroup()
	assert.Equal(t, "::group::Unit Tests\n::endgroup::\n", out.String())

	plain, out := newTestConsole(t, report.ConsoleConfig{}, report.LevelNormal)
	plain.Groupf("Unit Tests")
	plain.EndGroup()
	assert.Empty(t, out.String())

	plain.SetLevel(report.LevelVerbose)
	plain.Groupf("Unit Tests")
	assert.Equal(t, "\n-- Unit Tests --\n\n", out.String())
}
