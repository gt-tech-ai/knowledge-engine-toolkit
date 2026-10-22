package report

import (
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// quiet reports nothing.
type quiet struct{}

// OnGateStart does nothing.
func (quiet) OnGateStart(string, []string) {}

// OnPhaseStart does nothing.
func (quiet) OnPhaseStart(string, []string) {}

// OnStepStart does nothing.
func (quiet) OnStepStart(string, string) {}

// OnStepComplete does nothing.
func (quiet) OnStepComplete(types.StepResult) {}

// OnPhaseComplete does nothing.
func (quiet) OnPhaseComplete(string, types.StepResults, time.Duration) {}

// OnGateComplete does nothing.
func (quiet) OnGateComplete(string, types.StepResults, time.Duration) {}
