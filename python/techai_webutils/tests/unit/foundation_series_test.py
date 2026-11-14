"""Unit tests for the generated resilience series recorder.

# Test Coverage

The tests cover:
  - A recorder over no metrics provider records nothing and never raises.
  - A recorder registers each series with ``tier`` and ``name`` first and records the
    method's labels after them.
  - Two recorders over one registry share each series, apart by label.

# Test Structure

One class; the provider is either ``None`` or a real in-process ``CollectorRegistry``
behind ``new_metrics`` (no external dependency), so the exported series are the ones
an operator queries.
"""

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.foundation.metrics.metrics import new_metrics
from techai_webutils.foundation.resilience.series import (
    BreakerState,
    DeadLetterOutcome,
    HedgeOutcome,
    Reason,
    Recorder,
)

_TIER = "client"
_NAME = "payments"
_OP = "Get"
_OPEN_GAUGE = 2.0


def _sample(registry: CollectorRegistry, metric: str, labels: dict[str, str]) -> float:
    """Return a metric sample's value, 0 when the series does not exist.

    Args:
        registry: The registry the series lives in.
        metric: The sample's name.
        labels: The series' labels (``name`` is one of them, so not a keyword here).

    Returns:
        The sample's value, or 0 when the series does not exist.

    """
    return registry.get_sample_value(metric, labels) or 0.0


class TestRecorder:
    """The generated recorder binds its subject and records through the provider."""

    @staticmethod
    def test_recorder_without_metrics_records_nothing() -> None:
        """Test that a recorder over no provider is inert.

        Why this test is important:
          - every primitive and layer holds a recorder that may have no provider; a
            method that touched a missing instrument would turn "no metrics wired" into
            an error on the hot path.

        What it tests:
          - each method on ``Recorder(None, ...)`` returns without raising.
        """
        recorder = Recorder(None, _TIER, _NAME)

        recorder.retried(_OP)
        recorder.retry_exhausted(_OP)
        recorder.set_breaker_state(BreakerState.OPEN)
        recorder.breaker_moved(BreakerState.OPEN)
        recorder.rejected(_OP, Reason.RATE_LIMITED)
        recorder.timed_out(_OP)
        recorder.set_bulkhead_limit(1.0)
        recorder.set_throttle_rejection_ratio(1.0)
        recorder.hedged(HedgeOutcome.FIRED)
        recorder.dead_lettered(DeadLetterOutcome.SENT)
        recorder.deduplicated()

    @staticmethod
    def test_recorder_labels_tier_and_name_first() -> None:
        """Test that a recorder files each event under its subject and its labels.

        Why this test is important:
          - the labels are the series' identity; an event filed under the wrong subject
            or reason sends an operator to the wrong dependency.

        What it tests:
          - ``rejected`` counts ``resilience_rejections_total`` under tier, name,
            operation and reason; ``set_breaker_state`` sets the state's gauge number.
        """
        registry = CollectorRegistry()
        recorder = Recorder(new_metrics(registry), _TIER, _NAME)

        recorder.rejected(_OP, Reason.RATE_LIMITED)
        recorder.set_breaker_state(BreakerState.OPEN)

        rejections = _sample(
            registry,
            "resilience_rejections_total",
            {"tier": _TIER, "name": _NAME, "operation": _OP, "reason": "rate_limited"},
        )
        assert rejections == pytest.approx(1)
        state = _sample(
            registry, "resilience_breaker_state", {"tier": _TIER, "name": _NAME}
        )
        assert state == pytest.approx(_OPEN_GAUGE)

    @staticmethod
    def test_recorders_share_series_across_subjects() -> None:
        """Test that two recorders over one registry share each series.

        Why this test is important:
          - every decorated component builds its own recorder over the process registry;
            a second registration of the same series must reuse the first, not raise at
            boot.

        What it tests:
          - two recorders with different names each count one retry under their own name.
        """
        registry = CollectorRegistry()
        metrics = new_metrics(registry)

        Recorder(metrics, _TIER, _NAME).retried(_OP)
        Recorder(metrics, _TIER, "ledger").retried(_OP)

        for name in (_NAME, "ledger"):
            count = _sample(
                registry,
                "resilience_retries_total",
                {"tier": _TIER, "name": name, "operation": _OP},
            )
            assert count == pytest.approx(1)
