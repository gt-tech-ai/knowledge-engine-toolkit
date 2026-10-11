// Package channel provides a channel-based semaphore bulkhead implementation.
package channel

import (
	"context"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// ErrBulkheadFull is returned when the bulkhead semaphore is full.
var ErrBulkheadFull = apperr.New(
	apperr.CodeUnavailable,
	"bulkhead: concurrency limit reached",
)

// Compile-time interface assertion.
var _ interfaces.Bulkhead = (*Bulkhead)(nil)

// Config holds channel bulkhead settings.
type Config struct {
	// MaxConcurrent is the maximum number of concurrent operations allowed.
	MaxConcurrent int
}

// DefaultConfig returns sensible defaults for channel bulkhead.
func DefaultConfig() Config {
	return Config{
		MaxConcurrent: 10,
	}
}

// Bulkhead limits concurrent access to a resource using a channel semaphore.
type Bulkhead struct {
	// sem is a buffered channel used as a counting semaphore to limit concurrency.
	sem chan struct{}
}

// New creates a channel bulkhead from the given config.
func New(cfg Config) *Bulkhead {
	return &Bulkhead{sem: make(chan struct{}, cfg.MaxConcurrent)}
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
// Returns ErrBulkheadFull if no slots are available.
func (b *Bulkhead) TryExecute(fn func() error) error {
	select {
	case b.sem <- struct{}{}:
		defer func() { <-b.sem }()
		return fn()
	default:
		return ErrBulkheadFull
	}
}
