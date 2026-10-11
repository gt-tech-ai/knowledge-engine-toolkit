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

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/options"
)

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
	// Kind specifies which hedger implementation to use.
	Kind Kind

	// Delay is how long to wait for the first attempt to respond before firing the backup.
	Delay time.Duration `yaml:"delay" mapstructure:"delay"`
}

// DefaultConfig returns the default hedger configuration — disabled, because hedging is
// opt-in.
func DefaultConfig() Config {
	return Config{
		Kind:  KindDisabled,
		Delay: 50 * time.Millisecond,
	}
}

// WithDelay sets how long to wait before firing the backup attempt.
func WithDelay(d time.Duration) options.Option[Config] {
	return func(c *Config) { c.Delay = d }
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
		return &delayHedger{delay: cfg.Delay}, nil
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
	// delay is how long to wait before firing the backup attempt.
	delay time.Duration
}

// compile-time check: *delayHedger satisfies the Hedger contract.
var _ interfaces.Hedger = (*delayHedger)(nil)

// Hedge runs op, firing a second concurrent attempt after the configured delay and
// returning the first responder's result. The slower attempt's result is discarded; a
// buffered result channel avoids a leaked goroutine. To actually cancel the loser's work,
// op must observe ctx.
func (h *delayHedger) Hedge(ctx context.Context, op func() error) error {
	// Buffered for both attempts so the loser's goroutine never blocks on its send.
	results := make(chan error, 2)
	run := func() { go func() { results <- op() }() }

	run() // first attempt

	timer := time.NewTimer(h.delay)
	defer timer.Stop()

	select {
	case err := <-results:
		return err // first attempt beat the delay — no backup needed
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"hedged call canceled",
		)
	case <-timer.C:
		run() // delay elapsed — fire the backup and race the two attempts
	}

	select {
	case err := <-results:
		return err
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"hedged call canceled",
		)
	}
}
