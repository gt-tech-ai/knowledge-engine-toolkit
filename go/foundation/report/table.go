package report

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// table reports a result table and a TOTAL line per phase, then the gate's final
// table, its TOTAL line and the failure block.
type table struct {
	// p is the shared printer.
	p *printer
}

// OnGateStart prints nothing.
func (*table) OnGateStart(string, []string) {}

// OnPhaseStart prints nothing.
func (*table) OnPhaseStart(string, []string) {}

// OnStepStart prints nothing.
func (*table) OnStepStart(string, string) {}

// OnStepComplete prints nothing: steps show in the phase table.
func (*table) OnStepComplete(types.StepResult) {}

// OnPhaseComplete prints the phase's header, table and TOTAL line.
func (t *table) OnPhaseComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	t.p.headerf("%s", name)
	t.p.resultsTable(results)
	t.p.total(results, elapsed)
}

// OnGateComplete prints the gate's final header, table, TOTAL line and failure block.
func (t *table) OnGateComplete(
	name string,
	results types.StepResults,
	elapsed time.Duration,
) {
	t.p.headerf("%s - Final Results", name)
	t.p.resultsTable(results)
	t.p.total(results, elapsed)
	t.p.failureBlock(results)
}
