// Package decorators composes the Job decorator stack
// (ARCHITECTURE.md#decorator-order) around a scheduled job function:
// LeaderElection → RateLimit → Retry → Timeout → Tracing → Metrics → Logging →
// Job. The inner Retry/Timeout/observability reuses the shared client stack
// (go/clients/decorators), so the mechanism is composed once, not re-implemented.
package decorators

import (
	"context"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	clientstack "github.com/gt-tech-ai/knowledge-engine-toolkit/go/clients/decorators"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
)

// JobFunc is one run of a scheduled job.
type JobFunc func(ctx context.Context) error

// JobStackDeps carries the collaborators for the Job stack. A nil layer (or zero
// timeout) is skipped, so callers opt into exactly the concerns they have wired.
type JobStackDeps struct {
	// Leader gates execution: a non-leader instance skips the job (nil = always run).
	Leader interfaces.LeaderElector

	// Limiter bounds the job's start rate, blocking for a token (nil disables it).
	Limiter interfaces.RateLimiter

	// Retrier retries a transient job failure (nil disables retry).
	Retrier interfaces.Retrier

	// Tracer emits a span per job run (nil disables tracing).
	Tracer oteltrace.Tracer

	// Metrics records per-run counts/errors/latency (nil disables metrics).
	Metrics interfaces.Metrics

	// Logger records a failed run at Debug (nil disables the log).
	Logger interfaces.Logger

	// Name labels the job in metrics and span names.
	Name string

	// Timeout bounds each run attempt; zero disables the timeout layer.
	Timeout time.Duration
}

// WrapJob composes the Job stack (ARCHITECTURE.md#decorator-order) around fn, outermost →
// innermost:
// LeaderElection → RateLimit → [Retry → Timeout → Tracing → Metrics → Logging] → Job.
// A non-leader instance skips the run (returns nil), so a job scheduled across N replicas
// fires once; the rate limiter then smooths its start rate before the inner shared client
// stack applies retry/timeout/observability.
func WrapJob(fn JobFunc, deps JobStackDeps) JobFunc {
	stack := clientstack.New(deps.Name).
		WithRetrier(deps.Retrier).
		WithTimeout(deps.Timeout).
		WithTracer(deps.Tracer).
		WithMetrics(deps.Metrics).
		WithLogger(deps.Logger)

	return func(ctx context.Context) error {
		// LeaderElection (outermost): a non-leader instance does not run the job.
		if deps.Leader != nil {
			isLeader, err := deps.Leader.IsLeader(ctx)
			if err != nil {
				return err
			}
			if !isLeader {
				return nil
			}
		}
		// RateLimit: block for a token so the job's start rate stays bounded.
		if deps.Limiter != nil {
			if err := deps.Limiter.Wait(ctx); err != nil {
				return err
			}
		}
		// Inner: retry + timeout + observability via the shared client stack.
		_, err := clientstack.Run(ctx, stack, "job", clientstack.RunOpts{Retryable: true},
			func(c context.Context) (struct{}, error) { return struct{}{}, fn(c) })
		return err
	}
}
