// Package parsers holds the Parser kinds that turn a tool's output into a verdict:
// exit_code, gcc, json_lines and sarif. Every kind reads the exit code the same way: a
// tool that failed or did not start fails the verdict; a tool that exited 0 passes,
// or warns when its output still reported findings.
package parsers

import (
	"bytes"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

const (
	// KindExitCode decides by the exit code alone (also the empty kind).
	KindExitCode = "exit_code"

	// KindGCC reads `file:line[:col]: message` lines.
	KindGCC = "gcc"

	// KindJSONLines reads one JSON finding object per output line.
	KindJSONLines = "json_lines"

	// KindSARIF reads a SARIF 2.1 log's runs[].results[].
	KindSARIF = "sarif"
)

// NewFromConfig returns the parser kind names; an unknown kind is CodeInvalidInput.
func NewFromConfig(kind string) (interfaces.Parser, error) {
	switch kind {
	case "", KindExitCode:
		return exitCode{}, nil
	case KindGCC:
		return gcc{}, nil
	case KindJSONLines:
		return jsonLines{}, nil
	case KindSARIF:
		return sarif{}, nil
	default:
		return nil, apperr.New(apperr.CodeInvalidInput, "unknown output parser "+kind)
	}
}

// exitCode is KindExitCode.
type exitCode struct{}

// Parse decides by the exit code.
func (exitCode) Parse(res interfaces.CmdResult) types.Verdict {
	return verdict(res, nil)
}

// verdict applies the shared exit-code rule to res and the findings parsed from it.
// The detail is the tool's output, stdout then stderr, trimmed.
func verdict(res interfaces.CmdResult, findings []types.Finding) types.Verdict {
	v := types.Verdict{Findings: findings, Detail: output(res)}
	switch {
	case res.Err != nil || res.ExitCode != 0:
		v.Status = types.StatusFail
	case len(findings) > 0:
		v.Status = types.StatusWarn
	default:
		v.Status = types.StatusPass
	}
	return v
}

// output joins res's stdout and stderr, trimmed, in a fresh buffer so res is never
// written to; with neither, the error text.
func output(res interfaces.CmdResult) string {
	out := bytes.TrimSpace(bytes.Join(
		[][]byte{bytes.TrimSpace(res.Stdout), bytes.TrimSpace(res.Stderr)}, []byte("\n")))
	if len(out) == 0 && res.Err != nil {
		return res.Err.Error()
	}
	return string(out)
}
