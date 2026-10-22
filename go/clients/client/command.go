package client

import (
	"bytes"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// stderrTail bounds how much of a failed command's stderr an error carries.
const stderrTail = 2048

// Output returns a command's stdout, or a coded error saying what failed with the tail
// of its stderr: CodeUnavailable when the command could not start (exit -1), else
// CodeUpstream.
func Output(res interfaces.CmdResult, what string) ([]byte, error) {
	if res.Err == nil {
		return res.Stdout, nil
	}
	code := apperr.CodeUpstream
	if res.ExitCode == -1 {
		code = apperr.CodeUnavailable
	}
	msg := what
	if tail := bytes.TrimSpace(res.Stderr); len(tail) > 0 {
		msg += ": " + string(tail[max(0, len(tail)-stderrTail):])
	}
	return nil, apperr.Wrap(res.Err, code, msg)
}

// SplitNUL splits NUL-delimited command output (`git … -z`) into its non-empty
// fields.
func SplitNUL(out []byte) []string {
	var fields []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}
