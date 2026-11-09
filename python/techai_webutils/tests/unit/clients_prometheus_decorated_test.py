"""Unit tests for ``clients.prometheus.decorate``.

A metrics querier run through the client tier.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.prometheus import decorate
from techai_webutils.core.interfaces.metrics_query import MetricSample, MetricsQuerier
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations


@pytest.mark.asyncio
async def test_decorated_querier_forwards_queries_and_their_results() -> None:
    """Test that the decorated querier forwards each query and returns the inner answer.

    **Why this test is important:**
      - A consumer reads latency through the decorated querier; a wrapper that changed the
        query or dropped the answer would steer a governor or an alert on the wrong
        number.

    **What it tests:**
      - ``query`` and ``query_range`` reach the wrapped querier once each with the same
        arguments, and their results come back unchanged.
    """
    inner = AsyncMock(spec=MetricsQuerier)
    inner.query.return_value = 42.0
    start = datetime(2026, 11, 6, tzinfo=UTC)
    series = [MetricSample(time=start, value=1.0)]
    inner.query_range.return_value = series
    querier = decorate(
        inner,
        Decorations(decoration.default_config(), Collaborators()),
        "ingestion.metrics",
    )

    value = await querier.query("up")
    got = await querier.query_range("up", start, start, timedelta(seconds=15))

    assert value == pytest.approx(42.0)
    assert got == series
    inner.query.assert_awaited_once_with("up")
    inner.query_range.assert_awaited_once_with("up", start, start, timedelta(seconds=15))
