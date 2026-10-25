// Package ghactions is what a step running in GitHub Actions says to its runner: the
// markdown it appends to the job summary and the ::add-mask:: workflow commands that
// redact a value from the log.
package ghactions

import (
	"fmt"
	"io"
	"os"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// summaryMode is the job summary file's mode: the runner's own file, world-readable.
const summaryMode = 0o644

// AppendSummary appends markdown and a newline to the job summary file at path
// ($GITHUB_STEP_SUMMARY); an empty path (outside GitHub Actions) writes nothing.
func AppendSummary(path, markdown string) error {
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, summaryMode)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "open the job summary")
	}
	_, werr := f.WriteString(markdown + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return apperr.Wrap(werr, apperr.CodeInternal, "write the job summary")
	}
	return nil
}

// MaskEnv registers every value of env's NAME=value entries with the runner's log
// masking, one ::add-mask:: command per non-empty line of a value, written to w (the
// step's stdout). Printed anywhere but a GitHub Actions step, the commands show the
// values instead, so a caller writes them only there.
func MaskEnv(w io.Writer, env []string) error {
	for _, kv := range env {
		_, value, _ := strings.Cut(kv, "=")
		for line := range strings.SplitSeq(value, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if _, err := fmt.Fprintf(
				w,
				"::add-mask::%s\n",
				EscapeData(line),
			); err != nil {
				return apperr.Wrap(err, apperr.CodeInternal, "write the mask command")
			}
		}
	}
	return nil
}

// EscapeData escapes a workflow-command value the way the runner's @actions/core
// escapeData does (% → %25, CR → %0D, LF → %0A), so the value the runner unescapes is
// exactly v.
func EscapeData(v string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(v)
}
