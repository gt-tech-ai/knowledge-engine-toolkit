package mcp

import (
	"fmt"
	"strings"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// StepSummary is one step of a run as a tool result reports it.
type StepSummary struct {
	// Step is the step's name, unique within the result: a child step is named by its
	// path under its top-level step ("<parent>/<child>"), and a repeated name gets a
	// " #<n>" suffix.
	Step string `json:"step"`
	// Status is PASS, FAIL, SKIP, or WARN.
	Status string `json:"status"`
	// Duration is how long the step took (Go duration string).
	Duration string `json:"duration"`
	// Log is the path of the file holding the step's full output; empty when the step
	// printed nothing.
	Log string `json:"log,omitempty"`
}

// ToolResult is what an MCP tool call returns: every step's one-line summary, the
// output tail of each failed step, and the run's coded error.
type ToolResult struct {
	// FailedTails maps each failed step's (unique) name to the last tail_lines lines of
	// its captured output (its step error when it captured none); passing steps never
	// appear.
	FailedTails map[string]string `json:"failed_tails,omitempty"`
	// Error is the run's error as "CODE: message"; empty when the run succeeded.
	Error string `json:"error,omitempty"`
	// Steps lists every step the run reported, in completion order, each group step
	// followed by its children.
	Steps []StepSummary `json:"steps"`
}

// buildResult shapes a finished run as a ToolResult from its flattened steps: one
// summary per step with its log path from logs (aligned with steps), the last tailN
// output lines of each failed step, and err as a coded message.
func buildResult(
	steps []types.StepResult,
	tailN int,
	logs []string,
	err error,
) ToolResult {
	res := ToolResult{Steps: summarize(steps, logs)}
	for _, s := range steps {
		if s.Status != types.StatusFail {
			continue
		}
		if res.FailedTails == nil {
			res.FailedTails = map[string]string{}
		}
		res.FailedTails[s.Name] = failureText(s, tailN)
	}
	if err != nil {
		res.Error = codedMessage(err)
	}
	return res
}

// summarize returns one summary per flattened step, in order, with its log path from
// logs (aligned with steps; nil or short while the run has written none).
func summarize(steps []types.StepResult, logs []string) []StepSummary {
	out := make([]StepSummary, 0, len(steps))
	for i, s := range steps {
		sum := StepSummary{
			Step:     s.Name,
			Status:   s.Status.String(),
			Duration: s.Duration.String(),
		}
		if i < len(logs) {
			sum.Log = logs[i]
		}
		out = append(out, sum)
	}
	return out
}

// flatten lists each step followed by its children, depth first (the order the terminal
// renders them), with Children cleared on every copy. A child is named by its path under
// its top-level step ("<parent>/<child>"), and a name already used in the run gets a
// " #<n>" suffix, so every entry — and so every failed tail and log — is distinct even
// when several modules report a same-named package.
func flatten(steps []types.StepResult) []types.StepResult {
	var out []types.StepResult
	seen := map[string]int{}
	var walk func(prefix string, group []types.StepResult)
	walk = func(prefix string, group []types.StepResult) {
		for _, s := range group {
			name := s.Name
			if prefix != "" {
				name = prefix + "/" + s.Name
			}
			seen[name]++
			if seen[name] > 1 {
				name = fmt.Sprintf("%s #%d", name, seen[name])
			}
			children := s.Children
			s.Name, s.Children = name, nil
			out = append(out, s)
			walk(name, children)
		}
	}
	walk("", steps)
	return out
}

// failureText is what a failed step contributes to a result: the last n lines of its
// captured output, or its step error when its runner streamed the output instead of
// capturing it.
func failureText(s types.StepResult, n int) string {
	if len(s.Detail) == 0 {
		return s.Error
	}
	return tail(s.Detail, n)
}

// tail returns the last n lines of out, keeping its trailing newline; "" when n <= 0.
func tail(out []byte, n int) string {
	if n <= 0 {
		return ""
	}
	s := string(out)
	body := strings.TrimSuffix(s, "\n")
	cut := len(body)
	for range n {
		i := strings.LastIndexByte(body[:cut], '\n')
		if i < 0 {
			return s
		}
		cut = i
	}
	return s[cut+1:]
}

// codedMessage renders err as "CODE: message", adding the code its message lacks; an
// uncoded error renders as "UNKNOWN: message".
func codedMessage(err error) string {
	msg := err.Error()
	code := string(apperr.Code(err))
	if strings.HasPrefix(msg, code) {
		return msg
	}
	return fmt.Sprintf("%s: %s", code, msg)
}
