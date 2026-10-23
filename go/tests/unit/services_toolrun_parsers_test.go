package unit_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/services/toolrun/parsers"
)

// parseWith parses res with the parser kind.
func parseWith(t *testing.T, kind string, res interfaces.CmdResult) types.Verdict {
	t.Helper()
	p, err := parsers.NewFromConfig(kind)
	require.NoError(t, err)
	return p.Parse(res)
}

// TestToolrunParsers_ExitCode tests the exit-code rule every parser shares.
//
// Why this test is important:
//   - A failing tool must never pass, a tool that could not start must say why, and a
//     clean exit with reported findings must stay visible as a warning.
//
// What it tests:
//   - Exit 0 passes; a non-zero exit fails with the output as detail; a start failure
//     fails with the error text; findings on exit 0 warn.
func TestToolrunParsers_ExitCode(t *testing.T) {
	t.Parallel()
	assert.Equal(t, types.StatusPass, parseWith(t, "", interfaces.CmdResult{}).Status)
	v := parseWith(t, parsers.KindExitCode, interfaces.CmdResult{
		ExitCode: 1, Err: apperr.New(apperr.CodeInternal, "exit 1"),
		Stdout: []byte("out\n"), Stderr: []byte("err\n"),
	})
	assert.Equal(t, types.StatusFail, v.Status)
	assert.Equal(t, "out\nerr", v.Detail)
	v = parseWith(t, parsers.KindExitCode, interfaces.CmdResult{
		ExitCode: -1, Err: apperr.New(apperr.CodeNotFound, "no such binary"),
	})
	assert.Equal(t, types.StatusFail, v.Status)
	assert.Contains(t, v.Detail, "no such binary")
	v = parseWith(
		t,
		parsers.KindGCC,
		interfaces.CmdResult{Stdout: []byte("a.go:1: hint\n")},
	)
	assert.Equal(t, types.StatusWarn, v.Status)
}

// TestToolrunParsers_EmptyOutput tests the parser for tools whose listing is the
// finding.
//
// Why this test is important:
//   - `git status --porcelain` exits 0 whether or not the tree is clean; only its output
//     says whether there is something to report, so the exit code alone would pass a
//     dirty tree.
//
// What it tests:
//   - Exit 0 with no output passes; exit 0 with output fails with the output as detail;
//     a non-zero exit fails as under exit_code.
func TestToolrunParsers_EmptyOutput(t *testing.T) {
	t.Parallel()
	assert.Equal(
		t,
		types.StatusPass,
		parseWith(
			t,
			parsers.KindEmptyOutput,
			interfaces.CmdResult{Stdout: []byte("\n")},
		).Status,
	)
	v := parseWith(t, parsers.KindEmptyOutput, interfaces.CmdResult{
		Stdout: []byte(" M a.go\n?? b.go\n"),
	})
	assert.Equal(t, types.StatusFail, v.Status)
	assert.Equal(t, "M a.go\n?? b.go", v.Detail)
	v = parseWith(t, parsers.KindEmptyOutput, interfaces.CmdResult{
		ExitCode: 128, Err: apperr.New(apperr.CodeInternal, "exit 128"),
		Stderr: []byte("not a git repository"),
	})
	assert.Equal(t, types.StatusFail, v.Status)
	assert.Equal(t, "not a git repository", v.Detail)
}

// TestToolrunParsers_Formats tests the gcc, json_lines and sarif readers.
//
// Why this test is important:
//   - Findings carry the file and line a reviewer jumps to; a misread format loses
//     them, and noise lines must not become findings.
//
// What it tests:
//   - gcc reads file:line[:col] lines from stdout and stderr and skips others;
//     json_lines reads object lines and skips the rest; sarif reads rule, message,
//     file and line; non-SARIF output leaves the exit code to decide.
func TestToolrunParsers_Formats(t *testing.T) {
	t.Parallel()
	fail := interfaces.CmdResult{
		ExitCode: 1,
		Err:      apperr.New(apperr.CodeInternal, "exit 1"),
	}

	gccRes := fail
	gccRes.Stdout = []byte("# pkg\nx/a.go:12:5: undefined: foo\nnoise\n")
	gccRes.Stderr = []byte("b.c:3: warning: unused\n")
	v := parseWith(t, parsers.KindGCC, gccRes)
	assert.Equal(t, types.StatusFail, v.Status)
	assert.Equal(t, []types.Finding{
		{File: "x/a.go", Line: 12, Message: "undefined: foo"},
		{File: "b.c", Line: 3, Message: "warning: unused"},
	}, v.Findings)

	jl := fail
	jl.Stdout = []byte(`{"file":"a.py","line":2,"rule":"E1","message":"bad"}` +
		"\nnot json\n{\"other\":1}\n")
	assert.Equal(t, []types.Finding{{File: "a.py", Line: 2, Rule: "E1", Message: "bad"}},
		parseWith(t, parsers.KindJSONLines, jl).Findings)

	sr := fail
	sr.Stdout = []byte(`{"runs":[{"results":[{"ruleId":"R1","message":{"text":"m"},` +
		`"locations":[{"physicalLocation":{"artifactLocation":{"uri":"f.go"},` +
		`"region":{"startLine":7}}}]},{"ruleId":"R2","message":{"text":"n"}}]}]}`)
	assert.Equal(t, []types.Finding{
		{File: "f.go", Line: 7, Rule: "R1", Message: "m"},
		{Rule: "R2", Message: "n"},
	}, parseWith(t, parsers.KindSARIF, sr).Findings)
	bad := parseWith(t, parsers.KindSARIF, interfaces.CmdResult{Stdout: []byte("oops")})
	assert.Equal(t, types.StatusPass, bad.Status)
	assert.Empty(t, bad.Findings)

	_, err := parsers.NewFromConfig("xml")
	assert.Equal(t, apperr.CodeInvalidInput, apperr.Code(err))
}

// TestToolrunParsers_LeavesOutputIntact tests that parsing does not write into the
// command's output.
//
// Why this test is important:
//   - The runner hands the same result to the parser and to whatever logs or caches it;
//     a parser that joined stdout and stderr in place would overwrite the bytes after
//     stdout's trimmed end and corrupt the copy everyone else reads.
//
// What it tests:
//   - Parsing a result whose stdout ends in blank lines joins stdout and stderr in the
//     verdict and leaves the result's stdout byte-for-byte unchanged.
func TestToolrunParsers_LeavesOutputIntact(t *testing.T) {
	t.Parallel()
	stdout := []byte("out\n\n\n\n")
	res := interfaces.CmdResult{Stdout: stdout, Stderr: []byte("err\n")}
	v := parseWith(t, "", res)
	assert.Equal(t, "out\nerr", v.Detail)
	assert.Equal(t, "out\n\n\n\n", string(stdout))
}
