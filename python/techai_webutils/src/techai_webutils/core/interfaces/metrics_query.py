"""``MetricsQuerier``: evaluate PromQL against a metrics backend.

(mirrors Go ``interfaces.MetricsQuerier``).

A backend that measures fails closed: a value it cannot measure is an error, never a
    silent zero.
The zero-infrastructure stub is the deliberate exception: it measures nothing and answers
zero and an empty series, so the graph boots without a metrics server.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from datetime import datetime, timedelta


@dataclass(frozen=True, slots=True)
class MetricSample:
    """One point of a range-query series (Go ``types.MetricSample``)."""

    time: datetime
    """The sample's evaluation timestamp (UTC)."""
    value: float
    """The sample's finite value."""


class MetricsQuerier(ABC):
    """Evaluates metric queries (PromQL)."""

    @abstractmethod
    async def query(self, query: str) -> float:
        """Evaluate ``query`` as an instant query and return its single sample value."""
        ...

    @abstractmethod
    async def query_range(
        self, query: str, start: datetime, end: datetime, step: timedelta
    ) -> list[MetricSample]:
        """Evaluate ``query`` over ``[start, end]`` at ``step``; return its one series.

        (no series: empty).
        """
        ...
