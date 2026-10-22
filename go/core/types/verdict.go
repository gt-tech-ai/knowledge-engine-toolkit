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
	r := StepResult{
		Name:   name,
		Group:  group,
		Status: v.Status,
		Detail: []byte(v.render()),
	}
	if v.Status == StatusFail || v.Status == StatusWarn {
		r.Error = v.summary()
	}
	return r
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
	var b strings.Builder
	for _, f := range v.Findings {
		switch {
		case f.File != "" && f.Line > 0:
			fmt.Fprintf(&b, "%s:%d: ", f.File, f.Line)
		case f.File != "":
			b.WriteString(f.File + ": ")
		}
		if f.Rule != "" {
			b.WriteString(f.Rule + ": ")
		}
		b.WriteString(f.Message + "\n")
	}
	if v.Detail != "" {
		b.WriteString(v.Detail + "\n")
	}
	return b.String()
}
