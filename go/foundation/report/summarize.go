package report

import (
	"bytes"
	"strings"
)

// summaryWidth caps one condensed output line.
const summaryWidth = 200

// ActionableLines condenses a failed step's captured output to at most limit lines an
// operator can act on. It keeps the decisive lines first (Go test failures, file:line
// compiler and linter errors, pytest FAILED lines, the files a diff touches,
// govulncheck findings); when there are none it keeps the first lines that are not
// noise (see IsNoiseLine). Each line is trimmed and capped at 200 characters.
func ActionableLines(output []byte, limit int) []string {
	lines := nonEmptyLines(output)
	var priority []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- FAIL:"),
			strings.HasPrefix(line, "FAIL\t"),
			strings.HasPrefix(line, "FAIL "),
			isSourceError(line),
			strings.HasPrefix(line, "FAILED "),
			strings.HasPrefix(line, "Vulnerability #"):
			priority = append(priority, line)
		case strings.HasPrefix(line, "diff --git "):
			priority = append(priority, diffFile(line))
		}
		if len(priority) >= limit {
			break
		}
	}
	if len(priority) > 0 {
		for i := range priority {
			priority[i] = truncate(priority[i])
		}
		return priority
	}
	var kept []string
	for _, line := range lines {
		if IsNoiseLine(line) {
			continue
		}
		kept = append(kept, truncate(line))
		if len(kept) >= limit {
			break
		}
	}
	return kept
}

// IsNoiseLine reports whether a trimmed output line carries nothing actionable: a bare
// PASS/FAIL, runner decoration, Go test boilerplate, structured-log and timestamp
// lines, tool chatter, diff hunks and govulncheck's explanatory lines.
func IsNoiseLine(line string) bool {
	if line == "" || line == "FAIL" || line == "PASS" {
		return true
	}
	return isRunnerDecoration(line) ||
		isTestBoilerplate(line) ||
		isToolNoise(line) ||
		isDiffLine(line) ||
		isDiffContent(line) ||
		isVulncheckNoise(line)
}

// nonEmptyLines splits data into trimmed, non-empty lines.
func nonEmptyLines(data []byte) []string {
	var lines []string
	for raw := range bytes.SplitSeq(data, []byte("\n")) {
		if s := strings.TrimSpace(string(raw)); s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

// diffFile returns the b/ path of a "diff --git a/x b/x" header, or the header itself.
func diffFile(line string) string {
	if _, after, ok := strings.Cut(strings.TrimPrefix(line, "diff --git "), " b/"); ok {
		return after
	}
	return line
}

// truncate caps s at summaryWidth characters, marking a cut with "...".
func truncate(s string) string {
	if len(s) > summaryWidth {
		return s[:summaryWidth-3] + "..."
	}
	return s
}

// isRunnerDecoration matches a task runner's own progress glyph lines.
func isRunnerDecoration(line string) bool {
	return strings.HasPrefix(line, "▶ ") ||
		strings.HasPrefix(line, "✓ ") ||
		strings.HasPrefix(line, "✗ ") ||
		strings.HasPrefix(line, "── ")
}

// isTestBoilerplate matches `go test -v` progress and passing-package lines.
func isTestBoilerplate(line string) bool {
	return strings.HasPrefix(line, "=== RUN") ||
		strings.HasPrefix(line, "=== PAUSE") ||
		strings.HasPrefix(line, "=== CONT") ||
		strings.HasPrefix(line, "--- PASS") ||
		strings.Contains(line, "coverage:") ||
		strings.HasPrefix(line, "ok ") ||
		strings.HasPrefix(line, "ok\t")
}

// isToolNoise matches JSON log lines, RFC 3339-stamped lines, redis chatter, table
// rows, help text, pnpm lifecycle lines and echoed shell commands.
func isToolNoise(line string) bool {
	switch {
	case strings.HasPrefix(line, "{\""):
		return true
	case len(line) > 20 && line[4] == '-' && line[7] == '-' && line[10] == 'T':
		return true
	case strings.HasPrefix(line, "redis:"),
		strings.HasPrefix(line, "|"),
		strings.HasPrefix(line, "help:"),
		strings.HasPrefix(line, "ELIFECYCLE"),
		strings.HasPrefix(line, "$ "):
		return true
	}
	return false
}

// isDiffLine matches unified-diff headers (but not a "--- FAIL" test line).
func isDiffLine(line string) bool {
	return strings.HasPrefix(line, "+++ ") ||
		(strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "--- FAIL")) ||
		strings.HasPrefix(line, "@@ ") ||
		strings.HasPrefix(line, "index ")
}

// isDiffContent matches a diff's added or removed line.
func isDiffContent(line string) bool {
	if len(line) < 2 {
		return false
	}
	if line == "+" || line == "-" {
		return true
	}
	return (line[0] == '+' && line[1] != '+') || (line[0] == '-' && line[1] != '-')
}

// isVulncheckNoise matches govulncheck's trace frames and explanatory lines.
func isVulncheckNoise(line string) bool {
	if len(line) > 2 && line[0] == '#' && line[1] >= '0' && line[1] <= '9' {
		return true
	}
	for _, prefix := range []string{
		"More info:", "Standard library", "Found in:", "Fixed in:",
		"Example traces found:", "Your code is affected by", "This scan also found",
		"Use '-show",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// isSourceError matches a "<file>.<go|py|ts|tsx>:<line>" compiler or linter error,
// excluding call traces and diff lines that mention a file.
func isSourceError(line string) bool {
	if isCallTraceOrDiff(strings.TrimSpace(line)) {
		return false
	}
	for _, ext := range []string{".go:", ".py:", ".ts:", ".tsx:"} {
		if _, after, ok := strings.Cut(line, ext); ok && after != "" &&
			after[0] >= '0' && after[0] <= '9' {
			return true
		}
	}
	return false
}

// isCallTraceOrDiff matches diff lines, govulncheck frames and call-trace prose.
func isCallTraceOrDiff(line string) bool {
	if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") ||
		strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "diff --git") {
		return true
	}
	if len(line) > 2 && line[0] == '#' && line[1] >= '0' && line[1] <= '9' {
		return true
	}
	return strings.Contains(line, " calls ") ||
		strings.Contains(line, "which eventually calls")
}
