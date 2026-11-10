"""Tests for the MetricsLoggingObserver (sole per-item metric emitter for a fan-out)."""

import asyncio
from dataclasses import dataclass, field
from typing import cast
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

_EXPECTED_ITEM = 2
_EXPECTED_COMPLETED = 4


@dataclass
class _Accumulated:
    """What the recording instruments have folded in so far."""

    inflight: float = 0.0
    """The net inflight gauge value."""

    completed: float = 0.0
    """The summed ``ingest_completed_total`` increments."""

    failed: float = 0.0
    """The summed ``ingest_failed_total`` increments."""

    durations: list[float] = field(default_factory=list[float])
    """Every batch-duration observation, in order."""

    def set_inflight(self, value: float, **_labels: str) -> None:
        """Record a gauge ``set``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.inflight = value

    def add_inflight(self, value: float = 1.0, **_labels: str) -> None:
        """Record a gauge ``inc``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.inflight += value

    def sub_inflight(self, value: float = 1.0, **_labels: str) -> None:
        """Record a gauge ``dec``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.inflight -= value

    def add_completed(self, value: float = 1.0, **_labels: str) -> None:
        """Record a completed-counter ``inc``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.completed += value

    def add_failed(self, value: float = 1.0, **_labels: str) -> None:
        """Record a failed-counter ``inc``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.failed += value

    def observe(self, value: float, **_labels: str) -> None:
        """Record a histogram ``observe``.

        Args:
            value: The value.
            **_labels: Unused.

        """
        self.durations.append(value)


def _recording_metrics() -> tuple[MagicMock, _Accumulated]:
    """Build a spec-bound MetricsProvider mock whose instruments fold into one value.

    The mock hands the observer spec-bound gauge/counter/histogram mocks whose
    ``set``/``inc``/``dec``/``observe`` side-effects fold each call into ``acc`` — the
    mockist twin of a gomock ``.Do`` recorder. The test asserts on the *accumulated*
    values (net inflight gauge, summed counters, observation list), never on call spies,
    so the original gauge-nets-to-zero / summed-counter / one-observation assertions are
    preserved.

    Returns:
        The provider mock and the accumulated values its instruments update.

    """
    acc = _Accumulated()

    gauge = MagicMock(spec=MetricGauge)
    cast("MagicMock", gauge.set).side_effect = acc.set_inflight
    cast("MagicMock", gauge.inc).side_effect = acc.add_inflight
    cast("MagicMock", gauge.dec).side_effect = acc.sub_inflight

    completed = MagicMock(spec=MetricCounter)
    cast("MagicMock", completed.inc).side_effect = acc.add_completed

    failed = MagicMock(spec=MetricCounter)
    cast("MagicMock", failed.inc).side_effect = acc.add_failed

    histogram = MagicMock(spec=MetricHistogram)
    cast("MagicMock", histogram.observe).side_effect = acc.observe

    counters = {"ingest_completed_total": completed, "ingest_failed_total": failed}
    metrics = MagicMock(spec=MetricsProvider)
    cast("MagicMock", metrics.gauge).side_effect = (
        lambda _name, _help_text, _labels=None: gauge
    )
    cast("MagicMock", metrics.counter).side_effect = (
        lambda name, _help_text, _labels=None: counters[name]
    )
    cast("MagicMock", metrics.histogram).side_effect = (
        lambda _name, _help_text, _labels=None, _buckets=None: histogram
    )
    return metrics, acc


class TestMetricsLoggingObserver:
    """Tests for the metrics logging observer."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_emits_series_and_inflight_returns_to_zero() -> None:
        """Test that a driven fan-out emits completed/failed/duration and inflight nets.

        To zero.

        **Why this test is important:**
          - The observer is the *sole* per-item metric emitter; correct counts + a
            self-clearing inflight gauge are what make ingestion dashboards trustworthy. A
            double-count or a leaked inflight would silently corrupt on-call signals.

        **What it tests:**
          - Over a real fan_out of 4 items (1 failing): completed_total == 4 (no
            double-count), failed_total == 1, one batch-duration observation, and inflight
            returns to 0.0.
        """
        metrics, acc = _recording_metrics()
        observer = MetricsLoggingObserver(
            metrics, MagicMock(spec=Logger), subsystem="ingest"
        )

        async def fn(item: int) -> StepResult:
            """Fail the chosen item; pass every other.

            Args:
                item: The work item.

            Returns:
                The item's result.

            Raises:
                ValueError: For the chosen item.

            """
            await asyncio.sleep(0)
            if item == _EXPECTED_ITEM:
                msg = "boom"
                raise ValueError(msg)
            return StepResult(name=str(item))

        _ = await fan_out([0, 1, 2, 3], 2, fn, observer, name="poll")

        assert acc.inflight == pytest.approx(0.0)
        assert acc.completed == _EXPECTED_COMPLETED
        assert acc.failed == 1
        assert len(acc.durations) == 1
