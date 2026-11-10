package types

import (
	"fmt"
	"strings"
)

// Step renders v as the step named name in group: its status; for a failure or a
// warning, an error that counts the findings (or is the detail when there are none);
// and a detail listing the findings, one per line as `file:line: rule: message` with
// each part present only when set, followed by the verdict's own detail.
func (v Verdict) Step(name, group string) StepResult {
	stepResult := StepResult{
		Name:     name,
		Group:    group,
		Status:   v.Status,
		Detail:   []byte(v.render()),
		Error:    "",
		Children: nil,
		Duration: 0,
		Tests:    0,
		Output:   false,
	}
	if v.Status == StatusFail || v.Status == StatusWarn {
		stepResult.Error = v.summary()
	}

	return stepResult
}

// summary is the one-line error of a failing or warning verdict.
func (v Verdict) summary() string {
	switch len(v.Findings) {
	case 0:
		return v.Detail
	case 1:
		return "1 finding"
	default:
		return fmt.Sprintf("%d findings", len(v.Findings))
	}
}

// render lists the findings, then the detail.
func (v Verdict) render() string {
	if len(v.Findings) == 0 {
		return v.Detail
	}

	var builder strings.Builder

	for i := range v.Findings {
		writeFinding(&builder, &v.Findings[i])
	}

	if v.Detail != "" {
		_, _ = builder.WriteString(v.Detail + textNewline)
	}

	return builder.String()
}

// writeFinding writes one finding as `file:line: rule: message`, omitting unset parts.
func writeFinding(builder *strings.Builder, finding *Finding) {
	switch {
	case finding.File != "" && finding.Line > 0:
		fmt.Fprintf(builder, "%s:%d: ", finding.File, finding.Line)
	case finding.File != "":
		_, _ = builder.WriteString(finding.File + ": ")
	default:
		// No other value changes the outcome.
	}

	if finding.Rule != "" {
		_, _ = builder.WriteString(finding.Rule + ": ")
	}

	_, _ = builder.WriteString(finding.Message + textNewline)
}
