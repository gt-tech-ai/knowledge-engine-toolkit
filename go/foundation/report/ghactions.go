package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// ghActions writes GitHub Actions workflow commands: one log group per phase, an
// ::error annotation per failed step, and a gate verdict.
type ghActions struct {
	// w receives the workflow commands.
	w io.Writer

	// pending buffers the current phase's steps until it completes.
	pending types.StepResults
}

// OnGateStart does nothing: output is per phase and at the gate's end.
func (*ghActions) OnGateStart(string, []string) {}

// OnPhaseStart does nothing.
func (*ghActions) OnPhaseStart(string, []string) {}

// OnStepStart does nothing.
func (*ghActions) OnStepStart(string, string) {}

// OnStepComplete buffers the step for its phase.
func (g *ghActions) OnStepComplete(r types.StepResult) {
	g.pending = append(g.pending, r)
}

// OnPhaseComplete writes the phase's steps in a log group, then an annotation per
// failed step.
func (g *ghActions) OnPhaseComplete(name string, _ types.StepResults, _ time.Duration) {
	_, _ = fmt.Fprintf(g.w, "::group::%s\n", name)
	for _, r := range g.pending {
		_, _ = fmt.Fprintf(g.w, "  %-4s  %s\n", r.Status, r.Name)
	}
	_, _ = fmt.Fprintln(g.w, "::endgroup::")
	for _, r := range g.pending {
		if r.Status == types.StatusFail && r.Error != "" {
			_, _ = fmt.Fprintf(
				g.w,
				"::error title=%s::%s\n",
				escapeProperty(r.Name),
				escape(r.Error),
			)
		}
	}
	g.pending = g.pending[:0]
}

// OnGateComplete writes the gate's verdict as an error or a notice.
func (g *ghActions) OnGateComplete(_ string, results types.StepResults, _ time.Duration) {
	if n := results.CountByStatus()[types.StatusFail]; n > 0 {
		_, _ = fmt.Fprintf(g.w, "::error title=Gate::%d step(s) failed\n", n)
		return
	}
	_, _ = fmt.Fprintln(g.w, "::notice title=Gate::all steps passed")
}

// escape encodes the characters a workflow command's message may not carry.
func escape(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// escapeProperty encodes the characters a workflow command's property value (a title)
// may not carry: those of a message, plus the `:` and `,` that end a property.
func escapeProperty(s string) string {
	return strings.NewReplacer(":", "%3A", ",", "%2C").Replace(escape(s))
}
