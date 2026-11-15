// Package channel provides a channel-based semaphore bulkhead implementation.
package channel

import (
	"context"

	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/apperr"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/foundation/resilience/series"
)

// defaultConfigMaxConcurrent is the default MaxConcurrent of Config.
const defaultConfigMaxConcurrent = 10

// ErrBulkheadFull is returned when the bulkhead semaphore is full.
var ErrBulkheadFull = apperr.New(
	apperr.CodeUnavailable,
	"bulkhead: concurrency limit reached",
)

// Compile-time interface assertion.
var _ interfaces.Bulkhead = (*Bulkhead)(nil)

// Config holds channel bulkhead settings.
type Config struct {
	// Recorder counts each TryExecute refused for want of a slot as bulkhead_full with
	// no operation (a standalone bulkhead; inside a decorate tier the shedding layer
	// counts instead); nil records nothing. It is wired in code, never decoded from
	// config.
	Recorder *series.Recorder `yaml:"-" mapstructure:"-" json:"-"`

	// MaxConcurrent is the maximum number of concurrent operations allowed.
	MaxConcurrent int
}

// DefaultConfig returns sensible defaults for channel bulkhead.
func DefaultConfig() Config {
	return Config{
		Recorder:      nil,
		MaxConcurrent: defaultConfigMaxConcurrent,
	}
}

// Bulkhead limits concurrent access to a resource using a channel semaphore.
type Bulkhead struct {
	// sem is a buffered channel used as a counting semaphore to limit concurrency.
	sem chan struct{}

	// recorder counts refused TryExecute calls; nil records nothing.
	recorder *series.Recorder
}

// New creates a channel bulkhead from the given config.
func New(cfg Config) *Bulkhead {
	return &Bulkhead{sem: make(chan struct{}, cfg.MaxConcurrent), recorder: cfg.Recorder}
}

// Execute runs the function within the bulkhead's concurrency limit.
// Blocks until a slot is available or the context is cancelled.
func (b *Bulkhead) Execute(ctx context.Context, fn func() error) error {
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()

		return fn()
	case <-ctx.Done():
		return apperr.Wrap(
			ctx.Err(),
			apperr.ContextCode(ctx.Err(), apperr.CodeCanceled),
			"bulkhead wait canceled",
		)
	}
}

// TryExecute attempts to run the function without blocking.
// Returns ErrBulkheadFull if no slots are available, counted through Config.Recorder.
func (b *Bulkhead) TryExecute(fn func() error) error {
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()

		return fn()
	default:
		b.recorder.Rejected("", series.ReasonBulkheadFull)

		return ErrBulkheadFull
	}
}
