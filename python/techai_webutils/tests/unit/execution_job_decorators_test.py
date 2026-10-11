"""Tests for the job decorator stack (logging/metrics/tracing/recovery) + conditional composition."""

from __future__ import annotations

from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from techai_webutils.core.interfaces.execution import (
    BatchResult,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan
from techai_webutils.execution.job.conditional import when
from techai_webutils.execution.job.decorators import wrap


class _Job:
    """An in-test AnyJob returning a preset batch or raising (a contract fixture, not a fake dep)."""

    def __init__(
        self,
        result: BatchResult | None = None,
        *,
        raises: Exception | None = None,
        name: str = "j",
        group: str = "g",
        ran: list[str] | None = None,
    ) -> None:
        self._result = (
            result if result is not None else BatchResult(results=[StepResult(name="x")])
        )
        self._raises = raises
        self._name = name
        self._group = group
        self._ran = ran

    def meta(self) -> JobMeta:
        return JobMeta(name=self._name, group=self._group)

    async def execute(self) -> BatchResult:
        if self._ran is not None:
            self._ran.append(self._name)
        if self._raises is not None:
            raise self._raises
        return self._result


def _recording_metrics() -> tuple[MagicMock, SimpleNamespace]:
    """Build a spec-bound MetricsProvider mock; per-name instruments fold calls into a namespace.

    ``metrics.counter``/``metrics.histogram`` return the same spec-bound mock per name (mirroring
    the real ``setdefault`` registry), and each instrument's ``inc``/``observe`` side-effect
    records into ``acc.counter_calls[name]`` / ``acc.hist_obs[name]`` — so the test can assert on
    the recorded (value, labels) tuples and observation counts exactly as before.
    """
    counter_calls: dict[str, list[tuple[float, dict[str, str]]]] = {}
    hist_obs: dict[str, list[float]] = {}
    counter_cache: dict[str, MagicMock] = {}
    hist_cache: dict[str, MagicMock] = {}

    def _counter(
        name: str, _help_text: str, _labels: list[str] | None = None
    ) -> MagicMock:
        if name not in counter_cache:
            calls = counter_calls.setdefault(name, [])
            mock = MagicMock(spec=MetricCounter)
            mock.inc.side_effect = lambda value=1.0, **labels: calls.append((
                value,
                labels,
            ))
            counter_cache[name] = mock
        return counter_cache[name]

    def _histogram(name: str, *_args: object, **_kwargs: object) -> MagicMock:
        if name not in hist_cache:
            obs = hist_obs.setdefault(name, [])
            mock = MagicMock(spec=MetricHistogram)
            mock.observe.side_effect = lambda value, **_labels: obs.append(value)
            hist_cache[name] = mock
        return hist_cache[name]

    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter.side_effect = _counter
    metrics.histogram.side_effect = _histogram
    return metrics, SimpleNamespace(counter_calls=counter_calls, hist_obs=hist_obs)


def _recording_logger() -> tuple[MagicMock, list[tuple[str, str]]]:
    """Build a spec-bound Logger mock whose level methods append (level, message) to a list."""
    msgs: list[tuple[str, str]] = []
    logger = MagicMock(spec=Logger)
    for level in ("info", "warning", "error", "exception"):
        getattr(logger, level).side_effect = lambda msg, _level=level, **_kwargs: (
            msgs.append((_level, msg))
        )
    return logger, msgs


def _recording_tracer() -> tuple[MagicMock, SimpleNamespace]:
    """Build a spec-bound TracerProvider mock: ``span`` records its name and yields an error-recording span.

    ``span(name)`` appends ``name`` to ``acc.spans`` and returns a context manager yielding a
    spec-bound ``TracerSpan`` mock whose ``record_error`` appends to ``acc.errors``; ``__exit__``
    returns ``False`` so a raised exception still propagates (the tracing decorator re-raises).
    """
    acc = SimpleNamespace(spans=[], errors=[])
    span = MagicMock(spec=TracerSpan)
    span.record_error.side_effect = acc.errors.append
    span_cm = MagicMock()
    span_cm.__enter__.return_value = span
    span_cm.__exit__.return_value = False
    tracer = MagicMock(spec=TracerProvider)
    tracer.span.side_effect = lambda name, **_attributes: (
        acc.spans.append(name) or span_cm
    )
    return tracer, acc


class TestJobDecorators:
    """Tests for the job decorators."""

    @pytest.mark.asyncio
    async def test_no_decorators_returns_inner_unchanged(self) -> None:
        """Test that a chain with no decorators returns the inner job unchanged.

        **Why this test is important:**
          - The decorator builder must be zero-overhead when nothing is configured; wrapping an
            already-decorated or bare job with no options should not add a needless layer.

        **What it tests:**
          - wrap(job).build() is the same object as the inner job.
        """
        inner = _Job()
        assert wrap(inner).build() is inner

    @pytest.mark.asyncio
    async def test_recovery_converts_exception_to_failed_result(self) -> None:
        """Test that the recovery decorator turns a raised exception into a FAIL result.

        **Why this test is important:**
          - A raised exception in one poll must not crash the long-running worker; recovery is the
            structural guard that converts it to a failed step so the run loop continues.

        **What it tests:**
          - An inner job that raises yields a single FAIL result named for the job with the error text.
        """
        job = wrap(_Job(raises=RuntimeError("boom"))).with_recovery().build()
        batch = await job.execute()
        assert batch.has_failures is True
        assert batch.results[0].name == "j"
        assert "boom" in batch.results[0].error

    @pytest.mark.asyncio
    async def test_metrics_labels_outcome_and_records_duration(self) -> None:
        """Test that the metrics decorator records each run's outcome label and a duration.

        **Why this test is important:**
          - Per-job run counters + latency are how a worker's dashboards attribute throughput and
            failures; a wrong outcome label misreports the job's health.

        **What it tests:**
          - A passing then a failing run record outcomes ["ok", "failed"] and two duration samples.
        """
        metrics, acc = _recording_metrics()
        ok = (
            wrap(
                _Job(BatchResult(results=[StepResult(name="x", status=StepStatus.PASS)]))
            )
            .with_metrics(metrics)
            .build()
        )
        await ok.execute()
        bad = (
            wrap(
                _Job(
                    BatchResult(
                        results=[StepResult(name="x", status=StepStatus.FAIL, error="e")]
                    )
                ),
            )
            .with_metrics(metrics)
            .build()
        )
        await bad.execute()
        outcomes = [
            labels["outcome"]
            for _, labels in acc.counter_calls["execution_job_runs_total"]
        ]
        assert outcomes == ["ok", "failed"]
        assert len(acc.hist_obs["execution_job_duration_seconds"]) == 2

    @pytest.mark.asyncio
    async def test_logging_brackets_execution(self) -> None:
        """Test that the logging decorator logs a start and a finish line around a run.

        **Why this test is important:**
          - Start/finish log bracketing is the baseline observability for a job; missing either
            line breaks operators' ability to see a job began or completed.

        **What it tests:**
          - A successful run emits exactly two info lines (started, finished).
        """
        logger, msgs = _recording_logger()
        await wrap(_Job()).with_logger(logger).build().execute()
        assert [level for level, _ in msgs] == ["info", "info"]

    @pytest.mark.asyncio
    async def test_logging_warns_on_failed_result(self) -> None:
        """Test that a job finishing with failures logs a warning (not just an info) at completion.

        **Why this test is important:**
          - Operators triage on the failure log line; if a failed job logged the same info-level
            "finished" as a healthy one, failures would be invisible in the log stream.

        **What it tests:**
          - A job returning a FAIL result logs a start (info) then a finished-with-failures (warning).
        """
        logger, msgs = _recording_logger()
        failing = _Job(
            BatchResult(results=[StepResult(name="x", status=StepStatus.FAIL, error="e")])
        )
        await wrap(failing).with_logger(logger).build().execute()
        assert [level for level, _ in msgs] == ["info", "warning"]

    @pytest.mark.asyncio
    async def test_logging_logs_exception_and_reraises(self) -> None:
        """Test that a raising job is logged at exception level and the exception re-propagates.

        **Why this test is important:**
          - The logging decorator must not swallow a raise (recovery owns that) and must leave an
            exception-level record; a silently swallowed or unlogged raise loses the failure.

        **What it tests:**
          - A raising job logs a start (info) then an exception, and the RuntimeError still propagates.
        """
        logger, msgs = _recording_logger()
        job = wrap(_Job(raises=RuntimeError("boom"))).with_logger(logger).build()
        with pytest.raises(RuntimeError, match="boom"):
            await job.execute()
        assert [level for level, _ in msgs] == ["info", "exception"]

    @pytest.mark.asyncio
    async def test_tracing_opens_span_and_records_error(self) -> None:
        """Test that the tracing decorator opens a per-job span and records a raised error on it.

        **Why this test is important:**
          - Traces must bracket the actual execution and capture failures; a decorator that didn't
            record the error would lose the failure in the trace.

        **What it tests:**
          - A raising job opens span "job.j", records the error, and re-raises.
        """
        tracer, acc = _recording_tracer()
        job = wrap(_Job(raises=RuntimeError("x"))).with_tracing(tracer).build()
        with pytest.raises(RuntimeError):
            await job.execute()
        assert acc.spans == ["job.j"]
        assert len(acc.errors) == 1


class TestConditional:
    """Tests for the conditional ``when`` job."""

    @pytest.mark.asyncio
    async def test_when_false_skips_inner(self) -> None:
        """Test that a false condition skips the inner job with a SKIP result.

        **Why this test is important:**
          - A conditional job must not run its inner work when the condition is false; running it
            anyway would act on a precondition the gate meant to gate out.

        **What it tests:**
          - when(job, lambda: False) never runs the inner job and returns a single SKIP.
        """
        ran: list[str] = []
        job = when(_Job(ran=ran), lambda: False)
        batch = await job.execute()
        assert ran == []
        assert batch.results[0].status is StepStatus.SKIP

    @pytest.mark.asyncio
    async def test_when_true_runs_inner(self) -> None:
        """Test that a true condition runs the inner job.

        **Why this test is important:**
          - The conditional must run the inner job when the condition holds, or gated work would
            never execute.

        **What it tests:**
          - when(job, lambda: True) runs the inner job and returns its result.
        """
        ran: list[str] = []
        job = when(_Job(ran=ran), lambda: True)
        batch = await job.execute()
        assert ran == ["j"]
        assert batch.total == 1
