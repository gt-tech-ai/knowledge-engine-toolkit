package interfaces

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// Probe checks one property of a target and reports a verdict: a codebase invariant
// (a Probe[Codebase] is a guard), or a live system's health (a smoke check against an
// endpoint, a manifest check against a rendered chart).
type Probe[T any] interface {
	// Name identifies the probe; it names the probe's step in a set's results.
	Name() string

	// Run checks target. A probe reports what it found in the verdict and does not
	// return an error: a probe that cannot run fails, or marks the failure transient
	// when a retry may succeed.
	Run(ctx context.Context, target T) types.Verdict
}

// ProbeSet runs a set of probes against one target, one step per probe.
type ProbeSet[T any] interface {
	// Run runs every probe against target and returns their steps in probe order. The
	// error is set only when ctx ended before every probe finished.
	Run(ctx context.Context, target T) (types.StepResults, error)
}
