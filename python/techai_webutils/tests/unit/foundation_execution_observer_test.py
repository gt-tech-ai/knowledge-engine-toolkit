"""Tests for the MetricsLoggingObserver (sole per-item metric emitter for a fan-out)."""

import asyncio
from types import SimpleNamespace
from unittest.mock import MagicMock

import pytest

from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.core.interfaces.metrics import (
    MetricCounter,
    MetricGauge,
    MetricHistogram,
    MetricsProvider,
)
from techai_webutils.execution.engine.fan_out import fan_out
from techai_webutils.foundation.metrics.execution_observer import MetricsLoggingObserver


def _recording_metrics() -> tuple[MagicMock, SimpleNamespace]:
    """Build a spec-bound MetricsProvider mock whose instruments accumulate into a namespace.

    The mock hands the observer spec-bound gauge/counter/histogram mocks whose
    ``set``/``inc``/``dec``/``observe`` side-effects fold each call into ``acc`` — the mockist
    twin of a gomock ``.Do`` recorder. The test asserts on the *accumulated* values
    (net inflight gauge, summed counters, observation list), never on call spies, so the
    original gauge-nets-to-zero / summed-counter / one-observation assertions are preserved.
    """
    acc = SimpleNamespace(inflight=0.0, completed=0.0, failed=0.0, durations=[])

    gauge = MagicMock(spec=MetricGauge)

    def _set(value: float, **_labels: str) -> None:
        acc.inflight = value

    def _ginc(value: float = 1.0, **_labels: str) -> None:
        acc.inflight += value

    def _gdec(value: float = 1.0, **_labels: str) -> None:
        acc.inflight -= value

    gauge.set.side_effect = _set
    gauge.inc.side_effect = _ginc
    gauge.dec.side_effect = _gdec

    completed = MagicMock(spec=MetricCounter)
    completed.inc.side_effect = lambda value=1.0, **_labels: setattr(
        acc, "completed", acc.completed + value
    )

    failed = MagicMock(spec=MetricCounter)
    failed.inc.side_effect = lambda value=1.0, **_labels: setattr(
        acc, "failed", acc.failed + value
    )

    histogram = MagicMock(spec=MetricHistogram)
    histogram.observe.side_effect = lambda value, **_labels: acc.durations.append(value)

    counters = {"ingest_completed_total": completed, "ingest_failed_total": failed}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.gauge.side_effect = lambda _name, _help_text, _labels=None: gauge
    metrics.counter.side_effect = lambda name, _help_text, _labels=None: counters[name]
    metrics.histogram.side_effect = (
        lambda _name, _help_text, _labels=None, _buckets=None: histogram
    )
    return metrics, acc


class TestMetricsLoggingObserver:
    """Tests for the metrics logging observer."""

    @pytest.mark.asyncio
    async def test_emits_series_and_inflight_returns_to_zero(self) -> None:
        """Test that a driven fan-out emits completed/failed/duration and inflight nets to zero.

        **Why this test is important:**
          - The observer is the *sole* per-item metric emitter; correct counts + a self-clearing
            inflight gauge are what make ingestion dashboards trustworthy. A double-count or a
            leaked inflight would silently corrupt on-call signals.

        **What it tests:**
          - Over a real fan_out of 4 items (1 failing): completed_total == 4 (no double-count),
            failed_total == 1, one batch-duration observation, and inflight returns to 0.0.
        """
        metrics, acc = _recording_metrics()
        observer = MetricsLoggingObserver(
            metrics, MagicMock(spec=Logger), subsystem="ingest"
        )

        async def fn(item: int) -> StepResult:
            await asyncio.sleep(0)
            if item == 2:
                msg = "boom"
                raise ValueError(msg)
            return StepResult(name=str(item))

        await fan_out([0, 1, 2, 3], 2, fn, observer, name="poll")

        assert acc.inflight == pytest.approx(0.0)
        assert acc.completed == 4
        assert acc.failed == 1
        assert len(acc.durations) == 1
