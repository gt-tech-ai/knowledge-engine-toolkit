// Package loop runs background work as periodic workflow passes.
//
// A Ticker drives one pass, an interfaces.Workflow[struct{}, struct{}] the composition
// root has already decorated (recovery, tracing, metrics, logging), on a schedule:
// once per Interval, or back to back when Interval is zero. A failed pass is logged and
// retried after ErrorBackoff; the loop ends only when its context does. A Group starts a
// service's loops together, keeps one loop's failure from stopping the others, and waits
// for every loop on shutdown; it is an interfaces.Lifecycle, so a lifecycle.Manager can
// own it. Pass adapts a func(context.Context) error, such as a RunOnce method, into the
// workflow a Ticker runs.
package loop

import (
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
)

// Config schedules a Ticker.
type Config struct {
	// Interval is the time between passes. Zero runs passes back to back, for a pass
	// that already blocks on its source (a stream read with a block timeout).
	Interval time.Duration
	// ErrorBackoff is the wait after a failed pass before the loop continues. Zero
	// continues at once.
	ErrorBackoff time.Duration
	// RunAtStart runs one pass as soon as the loop starts instead of after the first
	// Interval.
	RunAtStart bool
}

// Validate rejects a negative Interval or ErrorBackoff with CodeInvalidInput.
func (c Config) Validate() error {
	if c.Interval < 0 {
		return apperr.New(apperr.CodeInvalidInput, "loop: interval must not be negative")
	}
	if c.ErrorBackoff < 0 {
		return apperr.New(
			apperr.CodeInvalidInput,
			"loop: error backoff must not be negative",
		)
	}
	return nil
}
