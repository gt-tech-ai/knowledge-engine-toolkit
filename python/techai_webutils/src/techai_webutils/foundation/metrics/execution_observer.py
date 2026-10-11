"""ExecutionObserver that emits Prometheus metrics + structured logs for a fan-out.

``MetricsLoggingObserver`` is the **sole per-item metric emitter** for a batch fan-out: the engine
funnels every completion through it exactly once, so counts never double-fire across retry/DLQ paths. It
is generic over ``subsystem`` (the metric-name prefix) so any batch service reuses it (e.g.
``subsystem="ingestion"``). It lives in ``foundation`` (not the core-only ``execution`` package) because
it depends on the metrics + logger foundations.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.execution import NopObserver, StepStatus

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.execution import BatchResult, StepResult
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider


class MetricsLoggingObserver(NopObserver):
    """Emit inflight gauge, completed/failed counters, batch-duration histogram + logs.

    Inflight is tracked as *accepted-but-not-yet-completed*: incremented by the batch size
    on start and decremented once per completion, so it returns to zero after a batch. It
    inherits :class:`NopObserver` so the gate/phase callbacks it does not emit fall back to
    no-ops — it remains the sole per-item metric emitter for the batch/step lifecycle.
    """

    def __init__(
        self, metrics: MetricsProvider, logger: Logger, *, subsystem: str
    ) -> None:
        """Create the metric series under ``subsystem`` and bind the logger."""
        # _log is the structured logger for batch/step lifecycle lines.
        self._log = logger
        # _subsystem is the metric-name prefix + a log field (e.g. "ingestion").
        self._subsystem = subsystem
        # _inflight gauges items accepted-but-not-yet-completed (nets to 0 per batch).
        self._inflight = metrics.gauge(
            f"{subsystem}_inflight",
            f"{subsystem} items accepted but not yet completed",
        )
        self._completed = metrics.counter(
            f"{subsystem}_completed_total",
            f"{subsystem} items completed, by status",
            ["status"],
        )
        self._failed = metrics.counter(
            f"{subsystem}_failed_total",
            f"{subsystem} items that failed",
        )
        self._duration = metrics.histogram(
            f"{subsystem}_batch_duration_seconds",
            f"{subsystem} batch wall-clock seconds",
        )

    @override
    def on_batch_start(self, name: str, total: int) -> None:
        """Record the batch size as inflight and log the batch start."""
        self._inflight.inc(float(total))
        self._log.info(
            "batch started", batch=name, subsystem=self._subsystem, total=total
        )

    @override
    def on_step_complete(self, result: StepResult) -> None:
        """Decrement inflight, count the completion by status, and count failures."""
        self._inflight.dec(1.0)
        self._completed.inc(1.0, status=result.status.value)
        if result.status is StepStatus.FAIL:
            self._failed.inc(1.0)
            self._log.warning(
                "step failed",
                step=result.name,
                subsystem=self._subsystem,
                error=result.error,
            )

    @override
    def on_batch_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Observe the batch wall-clock and log the aggregate outcome."""
        self._duration.observe(elapsed)
        self._log.info(
            "batch complete",
            batch=name,
            subsystem=self._subsystem,
            total=result.total,
            succeeded=result.succeeded,
            failed=result.failed,
            elapsed_seconds=round(elapsed, 4),
        )
