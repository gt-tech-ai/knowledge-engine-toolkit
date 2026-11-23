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

// TestActionableLines_TerraformError tests that a Terraform diagnostic printed after a
// long stdout is kept.
//
// Why this test is important:
//   - A failed tool row's output is stdout followed by stderr, and Terraform writes its
//     `Error:` diagnostic to stderr after the plan's refresh lines. Without a priority
//     rule the first ten stdout lines fill the block and the error is never shown.
//
// What it tests:
//   - A bare `Error:` line and a boxed one (`│ Error:`, also behind a terragrunt log
//     prefix) are kept, in order, ahead of the refresh lines before them.
func TestActionableLines_TerraformError(t *testing.T) {
	t.Parallel()

	refresh := strings.Repeat("data.aws_caller_identity.current: Reading...\n",
		actionableLinesInput+1)
	boxed := "│ Error: reading Secrets Manager Secret Version (a/b): not found"
	prefixed := "12:00:00.000 STDERR [auth0] terraform: │ Error: no valid credential sources"

	got := report.ActionableLines([]byte(refresh+"Error: Invalid value for variable\n"+
		"╷\n"+boxed+"\n│\n│   with data.aws_secretsmanager_secret_version.x,\n╵\n"+
		prefixed+"\n"), actionableLinesInput)

	assert.Equal(t, []string{"Error: Invalid value for variable", boxed, prefixed}, got)
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
	// actionableLinesInput is the limit passed to ActionableLines in TestActionableLines.
	actionableLinesInput = 10

	// actionableLinesInputLines is the limit passed to ActionableLines in
	// TestActionableLines.
	actionableLinesInputLines = 2

	// addInputActionableLines is the args passed to Add in FuzzActionableLines.
	addInputActionableLines = 5

	// addInputLines is the args passed to Add in FuzzActionableLines.
	addInputLines = 3

	// offsetLine is a slice bound of the input string in derivesLine.
	offsetLine = 197

	// repeatInputLines is the count passed to Repeat in TestActionableLines.
	repeatInputLines = 300

	// wantActionableLinesLen is the expected actionable lines len.
	wantActionableLinesLen = 2

	// wantInLen is the expected in len.
	wantInLen = 200

	// wantLimitCase is the expected limit case.
	wantLimitCase = 50
)
