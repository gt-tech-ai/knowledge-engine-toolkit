package loop

import (
	"context"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// Ticker runs one workflow pass on a schedule until its context ends.
type Ticker struct {
	// pass is the decorated workflow one iteration executes.
	pass interfaces.Workflow[struct{}, struct{}]
	// logger records failed passes.
	logger interfaces.Logger
	// name labels the loop in logs.
	name string
	// cfg is the validated schedule.
	cfg Config
}

// NewTicker returns a Ticker that runs pass on cfg's schedule, logging failures to
// logger under name. An invalid cfg is CodeInvalidInput; a nil pass or logger panics,
// as a wiring bug.
func NewTicker(
	name string,
	pass interfaces.Workflow[struct{}, struct{}],
	cfg Config,
	logger interfaces.Logger,
) (*Ticker, error) {
	if pass == nil || logger == nil {
		panic("loop: pass and logger must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Ticker{pass: pass, logger: logger, name: name, cfg: cfg}, nil
}

// Run executes passes until ctx ends, then returns nil. With a zero Interval passes
// run back to back; otherwise one runs per Interval, plus one at start when RunAtStart
// is set. Run satisfies Runner.
func (t *Ticker) Run(ctx context.Context) error {
	if t.cfg.Interval == 0 {
		for ctx.Err() == nil {
			t.once(ctx)
		}
		return nil
	}
	if t.cfg.RunAtStart {
		t.once(ctx)
	}
	tick := time.NewTicker(t.cfg.Interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			t.once(ctx)
		}
	}
}

// once runs one pass. A failure while ctx is live is logged and followed by the
// configured backoff; a failure caused by the shutdown is not a failure.
func (t *Ticker) once(ctx context.Context) {
	_, err := t.pass.Execute(ctx, struct{}{})
	if err == nil || ctx.Err() != nil {
		return
	}
	t.logger.Warn("loop pass failed", "loop", t.name, "error", err)
	if t.cfg.ErrorBackoff == 0 {
		return
	}
	wait := time.NewTimer(t.cfg.ErrorBackoff)
	defer wait.Stop()
	select {
	case <-ctx.Done():
	case <-wait.C:
	}
}
