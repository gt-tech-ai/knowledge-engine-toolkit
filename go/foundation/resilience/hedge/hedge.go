// Package hedge provides a builder for Hedger implementations that reduce tail latency by
// racing a backup attempt.
//
// Hedging is only safe for IDEMPOTENT / read-only operations — a second attempt must not
// double a side effect — so it is DISABLED by default and opted into per read path via
// configuration (KindDelay)..
//
//	h, err := hedge.New(hedge.KindDelay, hedge.WithDelay(50*time.Millisecond))
//	err = h.Hedge(ctx, func() error { return readFromKB() })
package hedge

import (
	"context"
	"fmt"
	"time"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// hedgeAttempts is the attempts one hedged call can race.
const hedgeAttempts = 2

// defaultConfigDelay is the default Delay of Config.
const defaultConfigDelay = 50 * time.Millisecond

// Kind specifies which hedger implementation to use.
type Kind int

const (
	// KindDisabled runs the operation exactly once (no hedging). The safe default: hedging
	// duplicates load and is only correct for idempotent operations.
	KindDisabled Kind = iota

	// KindDelay fires a second concurrent attempt after Delay and takes the first responder
	// (delay-then-race). Opt in only for idempotent / read-only paths.
	KindDelay
)

// String returns the string representation of Kind.
func (k Kind) String() string {
	switch k {
	case KindDisabled:
		return "disabled"
	case KindDelay:
		return "delay"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// Config is the superset configuration for all hedger kinds. Kind-incompatible fields are
// silently ignored.
type Config struct {
	// Recorder counts each backup the delay hedger fires and each one whose result it
	// returns; nil records nothing. It is wired in code (WithRecorder,
	// reswire.WithMetrics), never decoded from config.
	Recorder *series.Recorder `yaml:"-" mapstructure:"-" json:"-"`

	// Kind specifies which hedger implementation to use.
	Kind Kind

	// Delay is how long to wait for the first attempt to respond before firing the backup.
	Delay time.Duration `yaml:"delay" mapstructure:"delay"`
}

// DefaultConfig returns the default hedger configuration — disabled, because hedging is
// opt-in.
func DefaultConfig() Config {
	return Config{
		Recorder: nil,
		Kind:     KindDisabled,
		Delay:    defaultConfigDelay,
	}
}

// WithDelay sets how long to wait before firing the backup attempt.
func WithDelay(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.Delay = d }
}

// WithRecorder counts the hedger's fired and winning backups through recorder, bound to
// series.TierStandalone and the hedger's name by its caller.
func WithRecorder(recorder *series.Recorder) options.Option[Config] {
	return func(c *Config) { c.Recorder = recorder }
}

// New creates a Hedger of the specified kind with optional functional options. Returns an
// error if the kind is unknown.
func New(kind Kind, opts ...options.Option[Config]) (interfaces.Hedger, error) {
	cfg := DefaultConfig()

	cfg.Kind = kind
	options.ApplyOptions(&cfg, opts...)

	return NewFromConfig(cfg)
}

// NewFromConfig creates a Hedger from a Config struct. Returns an error if the kind is
// unknown.
func NewFromConfig(cfg Config) (interfaces.Hedger, error) {
	switch cfg.Kind {
	case KindDisabled:
		return disabledHedger{}, nil
	case KindDelay:
		return &delayHedger{delay: cfg.Delay, recorder: cfg.Recorder}, nil
	default:
		return nil, apperr.New(
			apperr.CodeInvalidInput,
			fmt.Sprintf("unknown hedger kind: %v", cfg.Kind),
		)
	}
}

// disabledHedger runs op exactly once (no hedging) — the safe default for non-idempotent
// paths.
type disabledHedger struct{}

// compile-time check: disabledHedger satisfies the Hedger contract.
var _ interfaces.Hedger = disabledHedger{}

// Hedge runs op once, without a backup attempt.
func (disabledHedger) Hedge(_ context.Context, op func() error) error {
	return op()
}

// delayHedger is the delay-then-race Hedger: it fires a backup attempt after delay and
// returns the first attempt to complete.
type delayHedger struct {
	// recorder counts fired and winning backups; nil records nothing.
	recorder *series.Recorder

	// delay is how long to wait before firing the backup attempt.
	delay time.Duration
}

// attempt is one attempt's result and whether it was the backup.
type attempt struct {
	// err is the attempt's result.
	err error

	// backup marks the attempt fired after the delay.
	backup bool
}

// compile-time check: *delayHedger satisfies the Hedger contract.
var _ interfaces.Hedger = (*delayHedger)(nil)

// Hedge runs op, firing a second concurrent attempt after the configured delay and
// returning the first responder's result. The slower attempt's result is discarded; a
// buffered result channel avoids a leaked goroutine. To actually cancel the loser's work,
// op must observe ctx. A fired backup is counted as fired, and as won when its result is
// the one returned.
func (h *delayHedger) Hedge(ctx context.Context, op func() error) error {
	// Buffered for both attempts so the loser's goroutine never blocks on its send.
	results := make(chan attempt, hedgeAttempts)
	run := func(backup bool) {
		go func() { results <- attempt{err: op(), backup: backup} }()
	}

	run(false) // first attempt

	timer := time.NewTimer(h.delay)
	defer timer.Stop()

	select {
	case first := <-results:
		return first.err // first attempt beat the delay — no backup needed
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"hedged call canceled",
		)
	case <-timer.C:
		run(true) // delay elapsed — fire the backup and race the two attempts
		h.recorder.Hedged(series.HedgeOutcomeFired)
	}

	select {
	case winner := <-results:
		if winner.backup {
			h.recorder.Hedged(series.HedgeOutcomeWon)
		}

		return winner.err
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"hedged call canceled",
		)
	}
}
