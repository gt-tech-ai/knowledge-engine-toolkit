package unit_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/report"
)

// TestActionableLines tests that ActionableLines keeps the lines that explain a
// failure and drops tool noise.
//
// Why this test is important:
//   - The failure block shows only these lines; promoting the wrong ones, or letting
//     noise through, buries the one line that says what broke.
//
// What it tests:
//   - Go test failures, file:line errors, pytest FAILED lines, govulncheck findings and
//     diff headers (reduced to the changed path) are kept in order ahead of preamble;
//     with none present, JSON logs, diff content, vulncheck prose and echoed commands
//     are dropped and an ordinary line survives; the result never exceeds limit and a
//     long line is capped at 200 characters.
func TestActionableLines(t *testing.T) {
	t.Parallel()

	got := report.ActionableLines([]byte("some preamble noise\n"+
		"--- FAIL: TestThing\n"+
		"FAIL\tpkg/foo\n"+
		"FAILED something broke\n"+
		"main.go:10:2: undefined: Foo\n"+
		"Vulnerability #1: GO-2024-0001\n"+
		"diff --git a/old.go b/new.go\n"), actionableLinesInput)
	assert.Equal(t, []string{
		"--- FAIL: TestThing", "FAIL\tpkg/foo", "FAILED something broke",
		"main.go:10:2: undefined: Foo", "Vulnerability #1: GO-2024-0001", "new.go",
	}, got)

	got = report.ActionableLines(
		[]byte("{\"level\":\"info\"}\n+added line\n-removed line\n"+
			"More info: https://example.com\n$ echo hello\n=== RUN TestX\na meaningful message\n"),
		actionableLinesInput,
	)
	assert.Equal(t, []string{"a meaningful message"}, got)

	assert.Len(
		t,
		report.ActionableLines(
			[]byte("a.go:1:1: x\nb.go:2:1: y\nc.go:3:1: z\n"),
			actionableLinesInputLines,
		),
		wantActionableLinesLen,
	)

	long := report.ActionableLines(
		[]byte(strings.Repeat(textX, repeatInputLines)),
		actionableLinesInput,
	)
	assert.Equal(t, []string{strings.Repeat(textX, 197) + "..."}, long)

	assert.True(t, report.IsNoiseLine("PASS"))
	assert.True(t, report.IsNoiseLine("ok  \tpkg/foo\t0.1s"))
	assert.False(t, report.IsNoiseLine("--- FAIL: TestThing"))
}

// FuzzActionableLines tests that ActionableLines only ever selects from its input.
//
// Why this test is important:
//   - The failure block must never invent text or exceed its line budget, whatever a
//     tool prints.
//
// What it tests:
//   - The result has at most limit lines, and every line is either a trimmed input
//     line, its 200-character cut, or the b-side path of a diff header in the input.
func FuzzActionableLines(f *testing.F) {
	f.Add("--- FAIL: T\nmain.go:1:2: x\n", addInputLines)
	f.Add("diff --git a/x b/y\n+z\n", 1)
	f.Add("{\"a\":1}\nplain\n", addInputActionableLines)
	f.Fuzz(func(t *testing.T, output string, limit int) {
		fuzzActionableLinesCase(t, output, limit)
	})
}

// fuzzActionableLinesCase is the body of a closure in FuzzActionableLines.
func fuzzActionableLinesCase(t *testing.T, output string, limit int) {
	t.Helper()

	if limit < 1 || limit > wantLimitCase {
		return
	}

	lines := report.ActionableLines([]byte(output), limit)
	assert.LessOrEqual(t, len(lines), limit)

	for _, l := range lines {
		assert.True(t, lineFromOutput(output, l), "line %q is not from the input", l)
	}
}

// lineFromOutput reports whether l is some output line, its truncation, or a diff target.
func lineFromOutput(output, l string) bool {
	for raw := range strings.SplitSeq(output, textNewline) {
		if derivesLine(strings.TrimSpace(raw), l) {
			return true
		}
	}

	return false
}

// derivesLine reports whether in yields l unchanged, truncated, or as a diff's b/ path.
func derivesLine(in, l string) bool {
	return in == l || (len(in) > wantInLen && l == in[:offsetLine]+"...") ||
		strings.HasPrefix(in, "diff --git ") && strings.HasSuffix(in, " b/"+l)
}

const (
	// actionableLinesInput is the actionable lines input the tests use.
	actionableLinesInput = 10

	// actionableLinesInputLines is the actionable lines input lines the tests use.
	actionableLinesInputLines = 2

	// addInputActionableLines is the add input actionable lines the tests use.
	addInputActionableLines = 5

	// addInputLines is the add input lines the tests use.
	addInputLines = 3

	// offsetLine is the offset line the tests use.
	offsetLine = 197

	// repeatInputLines is the repeat input lines the tests use.
	repeatInputLines = 300

	// wantActionableLinesLen is the expected actionable lines len.
	wantActionableLinesLen = 2

	// wantInLen is the expected in len.
	wantInLen = 200

	// wantLimitCase is the expected limit case.
	wantLimitCase = 50
)
