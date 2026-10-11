// Package decorators provides logging, metrics, tracing, and recovery wrappers
// for interfaces.AnyJob. They compose via Wrap(job).WithLogger(..).WithMetrics(..).
// WithTracing(..).WithRecovery().Build(), and are backward-compatible: a chain with
// no decorators returns the inner job unchanged.
package decorators

import (
	"context"
	"fmt"
	"time"

	apperr "github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/errors"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/interfaces"
	"github.com/gt-tech-ai/knowledge-engine-toolkit/go/core/types"
)

// loggingJob wraps an AnyJob with structured logging on start/finish.
type loggingJob struct {
	// inner is the wrapped job whose execution is logged.
	inner interfaces.AnyJob

	// logger receives the start, finish, and error log lines.
	logger interfaces.Logger
}

// Compile-time assertion that *loggingJob satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*loggingJob)(nil)

// Meta returns the wrapped job's metadata unchanged.
func (l *loggingJob) Meta() types.JobMeta { return l.inner.Meta() }

// Execute logs the job start, runs the inner job, then logs its outcome
// (error or finished-with-failure-flag) along with the elapsed wall-clock.
func (l *loggingJob) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	meta := l.inner.Meta()
	l.logger.WithContext(ctx).Info("job started", "name", meta.Name, "group", meta.Group)
	start := time.Now()
	results, err := l.inner.Execute(ctx, runner, root)
	elapsed := time.Since(start)
	if err != nil {
		l.logger.WithContext(ctx).
			Error("job error", "name", meta.Name, "elapsed", elapsed, "err", err)
	} else {
		l.logger.WithContext(ctx).Info(
			"job finished",
			"name",
			meta.Name,
			"elapsed",
			elapsed,
			"failures",
			results.HasFailures(),
		)
	}
	return results, err
}

// metricsJob wraps an AnyJob, recording run count (labeled by outcome) and
// duration, so every run of a periodic job emits run/error counters and a
// latency histogram.
type metricsJob struct {
	// inner is the wrapped job whose runs are measured.
	inner interfaces.AnyJob
	// metrics is the registry the counter/histogram are created on.
	metrics interfaces.Metrics
}

// Compile-time assertion that *metricsJob satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*metricsJob)(nil)

// Meta returns the wrapped job's metadata unchanged.
func (m *metricsJob) Meta() types.JobMeta { return m.inner.Meta() }

// Execute records the job's duration and an outcome-labeled run counter.
func (m *metricsJob) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	meta := m.inner.Meta()
	start := time.Now()
	results, err := m.inner.Execute(ctx, runner, root)
	elapsed := time.Since(start)

	outcome := "ok"
	switch {
	case err != nil:
		outcome = "error"
	case results.HasFailures():
		outcome = "failed"
	}
	m.metrics.Counter(
		"execution_job_runs_total", "Job runs by name and outcome", "job", "outcome",
	).
		Add(1, meta.Name, outcome)
	m.metrics.Histogram(
		"execution_job_duration_seconds", "Job duration in seconds", nil, "job",
	).
		Observe(elapsed.Seconds(), meta.Name)
	return results, err
}

// tracingJob wraps an AnyJob in a span, recording errors on it.
type tracingJob struct {
	// inner is the wrapped job whose execution is traced.
	inner interfaces.AnyJob
	// tracer starts the per-run span.
	tracer interfaces.Tracer
}

// Compile-time assertion that *tracingJob satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*tracingJob)(nil)

// Meta returns the wrapped job's metadata unchanged.
func (t *tracingJob) Meta() types.JobMeta { return t.inner.Meta() }

// Execute runs the inner job within a span named for the job.
func (t *tracingJob) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (types.StepResults, error) {
	meta := t.inner.Meta()
	ctx, span := t.tracer.Start(ctx, "job."+meta.Name)
	defer span.End()
	results, err := t.inner.Execute(ctx, runner, root)
	if err != nil {
		span.RecordError(err)
	}
	return results, err
}

// recoveryJob wraps an AnyJob, converting a panic into an error + a failed
// StepResults so one panicking sweep run never crashes the long-running worker.
type recoveryJob struct {
	// inner is the wrapped job whose panics are recovered.
	inner interfaces.AnyJob
	// logger, when set, records the recovered panic (may be nil).
	logger interfaces.Logger
}

// Compile-time assertion that *recoveryJob satisfies interfaces.AnyJob.
var _ interfaces.AnyJob = (*recoveryJob)(nil)

// Meta returns the wrapped job's metadata unchanged.
func (r *recoveryJob) Meta() types.JobMeta { return r.inner.Meta() }

// Execute runs the inner job, recovering any panic into a returned error.
func (r *recoveryJob) Execute(
	ctx context.Context,
	runner interfaces.CommandRunner,
	root string,
) (results types.StepResults, err error) {
	meta := r.inner.Meta()
	defer func() {
		if rec := recover(); rec != nil {
			if r.logger != nil {
				r.logger.WithContext(ctx).
					Error("job panic recovered", "name", meta.Name, "panic", rec)
			}
			err = apperr.New(
				apperr.CodeInternal,
				fmt.Sprintf("job %q panicked: %v", meta.Name, rec),
			)
			results = types.StepResults{{
				Name:   meta.Name,
				Group:  meta.Group,
				Status: types.StatusFail,
				Error:  err.Error(),
			}}
		}
	}()
	return r.inner.Execute(ctx, runner, root)
}

// Builder wraps an AnyJob with optional logging, metrics, tracing, and recovery
// decorators.
type Builder struct {
	// inner is the base job being decorated.
	inner interfaces.AnyJob

	// logger, when set via WithLogger, adds the logging decorator on Build.
	logger interfaces.Logger

	// metrics, when set via WithMetrics, adds the metrics decorator on Build.
	metrics interfaces.Metrics

	// tracer, when set via WithTracing, adds the tracing decorator on Build.
	tracer interfaces.Tracer

	// recover, when set via WithRecovery, adds the panic-recovery decorator on Build.
	recover bool
}

// Wrap starts a decoration chain for job.
func Wrap(job interfaces.AnyJob) *Builder { return &Builder{inner: job} }

// WithLogger adds structured logging around the job.
func (b *Builder) WithLogger(logger interfaces.Logger) *Builder {
	b.logger = logger
	return b
}

// WithMetrics adds run-count + duration metrics around the job.
func (b *Builder) WithMetrics(metrics interfaces.Metrics) *Builder {
	b.metrics = metrics
	return b
}

// WithTracing adds a per-run span around the job.
func (b *Builder) WithTracing(tracer interfaces.Tracer) *Builder {
	b.tracer = tracer
	return b
}

// WithRecovery adds panic→error recovery (innermost, so logging/metrics/tracing
// still observe the converted failure).
func (b *Builder) WithRecovery() *Builder {
	b.recover = true
	return b
}

// Build returns the decorated job. Decorators are layered innermost→outermost as
// recovery → logging → metrics → tracing, so a panic is converted first and the
// outer layers observe it as a normal failure. A chain with no decorators returns
// the inner job unchanged.
func (b *Builder) Build() interfaces.AnyJob {
	result := b.inner
	if b.recover {
		result = &recoveryJob{inner: result, logger: b.logger}
	}
	if b.logger != nil {
		result = &loggingJob{inner: result, logger: b.logger}
	}
	if b.metrics != nil {
		result = &metricsJob{inner: result, metrics: b.metrics}
	}
	if b.tracer != nil {
		result = &tracingJob{inner: result, tracer: b.tracer}
	}
	return result
}
