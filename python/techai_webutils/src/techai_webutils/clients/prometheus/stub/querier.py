"""``StubMetricsQuerier``: answers zero and an empty series with no I/O (the default)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.metrics_query import MetricSample, MetricsQuerier

if TYPE_CHECKING:
    from datetime import datetime, timedelta


class StubMetricsQuerier(MetricsQuerier):
    """Measures nothing, so the graph boots without a metrics server."""

    @override
    async def query(self, query: str) -> float:
        """Return 0.

        Returns:
            0.

        """
        return 0.0

    @override
    async def query_range(
        self, query: str, start: datetime, end: datetime, step: timedelta
    ) -> list[MetricSample]:
        """Return an empty series.

        Returns:
            An empty series.

        """
        return []
