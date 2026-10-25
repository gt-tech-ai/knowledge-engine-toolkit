package unit_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/ghactions"
)

// TestFoundationGhactions tests the job summary append and the log-mask commands.
//
// Why this test is important:
//   - a plan or drift summary reaches a reviewer only through the job summary file the
//     runner provides, and a credential a step injects into a child is redacted from the
//     job log only when the step registers it with ::add-mask:: first, escaped the way
//     the runner unescapes it; a multi-line value (a PEM key) must be masked line by
//     line, since the runner masks whole lines.
//
// What it tests:
//   - AppendSummary creates the file, appends each markdown with a trailing newline and
//     writes nothing for an empty path; an unwritable path is INTERNAL; MaskEnv writes
//     one command per non-empty trimmed line of each value, with %, CR and LF escaped.
func TestFoundationGhactions(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "summary.md")
	require.NoError(t, ghactions.AppendSummary(path, "### one"))
	require.NoError(t, ghactions.AppendSummary(path, "### two"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "### one\n### two\n", string(data))
	require.NoError(t, ghactions.AppendSummary("", "nowhere"))
	err = ghactions.AppendSummary(filepath.Join(path, "nested"), "x")
	require.Equal(t, apperr.CodeInternal, apperr.Code(err))

	var out bytes.Buffer
	require.NoError(t, ghactions.MaskEnv(&out, []string{
		"AUTH0_CLIENT_SECRET=s3cr3t%",
		"GITHUB_APP_KEY=-----BEGIN KEY-----\n abc \n\n-----END KEY-----",
		"EMPTY=",
	}))
	assert.Equal(t, "::add-mask::s3cr3t%25\n::add-mask::-----BEGIN KEY-----\n"+
		"::add-mask::abc\n::add-mask::-----END KEY-----\n", out.String())
	assert.Equal(t, "a%25b%0Dc%0Ad", ghactions.EscapeData("a%b\rc\nd"))
}

// TestGhactions_TruncateComment tests the head-and-tail cut that fits a long markdown
// body into GitHub's comment limit.
//
// Why this test is important:
//   - A plan summary larger than the comment limit would be rejected by GitHub, leaving
//     the PR with no plan at all; the head (totals, destroys) and the tail (the last
//     units) are what a reviewer needs, so both must survive the cut, and a cut inside
//     a code fence or a <details> block would swallow the notice and the tail into the
//     block.
//
// What it tests:
//   - A body within the limit (one exactly at it included) is unchanged.
//   - A longer body comes back within the limit, counted in characters (a multi-byte
//     body is not over-cut), starting with the head, ending with the tail, with a notice
//     naming exactly how many characters were removed in between.
//   - A cut inside a code fence or a <details> block closes it before the notice and
//     reopens it after, so the blocks stay balanced on both sides.
//   - A limit too small for the notice keeps only the first limit characters.
func TestGhactions_TruncateComment(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "short", ghactions.TruncateComment("short", 10))
	assert.Equal(t, "exactly10!", ghactions.TruncateComment("exactly10!", 10))

	long := "HEAD-" + strings.Repeat("é", 1000) + "-TAIL"
	got := ghactions.TruncateComment(long, 400)
	assert.LessOrEqual(t, utf8.RuneCountInString(got), 400)
	head, rest, ok := strings.Cut(got, "\n\n… ")
	require.True(t, ok, got)
	count, _, ok := strings.Cut(rest, " characters cut")
	require.True(t, ok, got)
	_, tail, ok := strings.Cut(rest, " …\n\n")
	require.True(t, ok, got)
	kept := utf8.RuneCountInString(head) + utf8.RuneCountInString(tail)
	assert.Equal(t, strconv.Itoa(utf8.RuneCountInString(long)-kept), count,
		"the notice names exactly the characters removed")
	assert.Greater(t, utf8.RuneCountInString(got), 300, "cut by characters, not bytes")
	assert.True(t, strings.HasPrefix(got, "HEAD-é"), got)
	assert.True(t, strings.HasSuffix(got, "é-TAIL"), got)
	assert.Contains(t, got, "characters cut to fit GitHub's comment limit")

	fenced := "<details>\n```\n" + strings.Repeat("x", 1000) + "\n```\n</details>"
	got = ghactions.TruncateComment(fenced, 400)
	assert.LessOrEqual(t, utf8.RuneCountInString(got), 400)
	before, after, ok := strings.Cut(got, "characters cut")
	require.True(t, ok, got)
	assert.Zero(
		t,
		strings.Count(before, "```")%2,
		"the head's fence is closed: %q",
		before,
	)
	assert.Equal(
		t,
		strings.Count(before, "<details>"),
		strings.Count(before, "</details>"),
	)
	assert.Zero(t, strings.Count(after, "```")%2, "the tail's fence is reopened")
	assert.Equal(t, strings.Count(after, "<details>"), strings.Count(after, "</details>"))

	assert.Equal(t, "HEAD", ghactions.TruncateComment(long, 4))
}
