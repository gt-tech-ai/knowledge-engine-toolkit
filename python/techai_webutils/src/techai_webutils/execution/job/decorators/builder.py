"""Job decorators: logging/metrics/tracing/recovery AnyJob wrappers + a Wrap builder.

The async port of Go ``job/decorators`` — compose via
``wrap(job).with_logger(..).with_metrics(..).with_tracing(..).with_recovery().build()``. Decorators
layer innermost->outermost as recovery -> logging -> metrics -> tracing, so a raised exception is
converted to a failure first and the outer layers observe it as a normal outcome. A chain with no
decorators returns the inner job unchanged.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Self

from techai_webutils.core.interfaces.execution import BatchResult, StepResult, StepStatus

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.execution import AnyJob, JobMeta
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import (
        MetricCounter,
        MetricHistogram,
        MetricsProvider,
    )
    from techai_webutils.core.interfaces.tracer import TracerProvider


class _LoggingJob:
    """Wraps an AnyJob with structured start/finish/error logging (mirrors Go ``loggingJob``)."""

    def __init__(self, inner: AnyJob, logger: Logger) -> None:
        self._inner: AnyJob = inner
        self._logger: Logger = logger

    def meta(self) -> JobMeta:
        """Return the wrapped job's metadata unchanged."""
        return self._inner.meta()

    async def execute(self) -> BatchResult:
        """Log the job start, run it, then log the outcome + elapsed (error log + re-raise on failure)."""
        meta = self._inner.meta()
        self._logger.info("job started", name=meta.name, group=meta.group)
        loop = asyncio.get_running_loop()
        start = loop.time()
        try:
            result = await self._inner.execute()
        except Exception as exc:
            self._logger.exception(
                "job error", name=meta.name, elapsed=loop.time() - start, error=str(exc)
            )
            raise
        elapsed = loop.time() - start
        if result.has_failures:
            self._logger.warning(
                "job finished with failures", name=meta.name, elapsed=elapsed
            )
        else:
            self._logger.info("job finished", name=meta.name, elapsed=elapsed)
        return result


class _MetricsJob:
    """Wraps an AnyJob, recording an outcome-labeled run counter + duration (mirrors Go ``metricsJob``)."""

    def __init__(self, inner: AnyJob, metrics: MetricsProvider) -> None:
        self._inner: AnyJob = inner
        self._runs: MetricCounter = metrics.counter(
            "execution_job_runs_total",
            "Job runs by name and outcome",
            ["job", "outcome"],
        )
        self._duration: MetricHistogram = metrics.histogram(
            "execution_job_duration_seconds",
            "Job duration in seconds",
            labels=["job"],
        )

    def meta(self) -> JobMeta:
        """Return the wrapped job's metadata unchanged."""
        return self._inner.meta()

    async def execute(self) -> BatchResult:
        """Run the job, recording its duration and an outcome-labeled run counter.

        The outcome is ``ok`` / ``failed`` (from the result) or ``error`` when the inner job *raises*.
        Note ``error`` is only reachable when recovery is NOT wired: the standard
        ``recovery -> logging -> metrics -> tracing`` stack converts a raised exception into a FAIL
        result innermost, so metrics then observes ``failed`` (not ``error``).
        """
        meta = self._inner.meta()
        loop = asyncio.get_running_loop()
        start = loop.time()
        try:
            result = await self._inner.execute()
        except Exception:
            self._record(meta.name, "error", loop.time() - start)
            raise
        self._record(
            meta.name, "failed" if result.has_failures else "ok", loop.time() - start
        )
        return result

    def _record(self, job: str, outcome: str, elapsed: float) -> None:
        """Emit the run counter (by outcome) and the duration histogram."""
        self._runs.inc(1.0, job=job, outcome=outcome)
        self._duration.observe(elapsed, job=job)


class _TracingJob:
    """Wraps an AnyJob in a per-run span, recording errors on it (mirrors Go ``tracingJob``)."""

    def __init__(self, inner: AnyJob, tracer: TracerProvider) -> None:
        self._inner: AnyJob = inner
        self._tracer: TracerProvider = tracer

    def meta(self) -> JobMeta:
        """Return the wrapped job's metadata unchanged."""
        return self._inner.meta()

    async def execute(self) -> BatchResult:
        """Run the inner job within a span named for the job, recording a raised error on it."""
        meta = self._inner.meta()
        with self._tracer.span(f"job.{meta.name}") as span:
            try:
                return await self._inner.execute()
            except Exception as exc:
                span.record_error(exc)
                raise


class _RecoveryJob:
    """Wraps an AnyJob, converting a raised exception into a FAIL result (mirrors Go ``recoveryJob``)."""

    def __init__(self, inner: AnyJob, logger: Logger | None = None) -> None:
        self._inner: AnyJob = inner
        self._logger: Logger | None = logger

    def meta(self) -> JobMeta:
        """Return the wrapped job's metadata unchanged."""
        return self._inner.meta()

    async def execute(self) -> BatchResult:
        """Run the inner job, converting a non-cancellation exception into a single FAIL result."""
        meta = self._inner.meta()
        try:
            return await self._inner.execute()
        except asyncio.CancelledError:
            raise  # never swallow cancellation
        except Exception as exc:
            if self._logger is not None:
                self._logger.exception(
                    "job exception recovered", name=meta.name, error=str(exc)
                )
            return BatchResult(
                results=(
                    StepResult(
                        name=meta.name,
                        group=meta.group,
                        status=StepStatus.FAIL,
                        error=str(exc),
                    ),
                ),
            )


class DecoratorBuilder:
    """Fluent builder wrapping an AnyJob with optional logging/metrics/tracing/recovery decorators."""

    def __init__(self, inner: AnyJob) -> None:
        """Start a decoration chain for ``inner``."""
        self._inner: AnyJob = inner
        self._logger: Logger | None = None
        self._metrics: MetricsProvider | None = None
        self._tracer: TracerProvider | None = None
        self._recover: bool = False

    def with_logger(self, logger: Logger) -> Self:
        """Add structured start/finish/error logging around the job."""
        self._logger = logger
        return self

    def with_metrics(self, metrics: MetricsProvider) -> Self:
        """Add run-count + duration metrics around the job."""
        self._metrics = metrics
        return self

    def with_tracing(self, tracer: TracerProvider) -> Self:
        """Add a per-run span around the job."""
        self._tracer = tracer
        return self

    def with_recovery(self) -> Self:
        """Add exception -> FAIL recovery (innermost, so logging/metrics/tracing observe the failure)."""
        self._recover = True
        return self

    def build(self) -> AnyJob:
        """Return the decorated job, layered recovery -> logging -> metrics -> tracing.

        A chain with no decorators returns the inner job unchanged.
        """
        result: AnyJob = self._inner
        if self._recover:
            result = _RecoveryJob(result, self._logger)
        if self._logger is not None:
            result = _LoggingJob(result, self._logger)
        if self._metrics is not None:
            result = _MetricsJob(result, self._metrics)
        if self._tracer is not None:
            result = _TracingJob(result, self._tracer)
        return result


def wrap(job: AnyJob) -> DecoratorBuilder:
    """Start a decoration chain for ``job`` (mirrors Go ``decorators.Wrap``)."""
    return DecoratorBuilder(job)
