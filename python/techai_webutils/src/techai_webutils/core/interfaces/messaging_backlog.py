"""The ``BacklogProbe`` consumer capability: how much work is waiting on a topic.

Mirrors Go's ``interfaces.BacklogProbe``. Alerts and autoscaling read the broker-neutral
gauges this feeds (``messaging_backlog_messages`` / ``messaging_oldest_age_seconds``), not
a broker's own metrics, so moving to another broker keeps the dashboards and the scaler.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass


@dataclass(frozen=True)
class Backlog:
    """The work waiting on one topic."""

    messages: int
    """Messages waiting to be received (SQS: ``ApproximateNumberOfMessages``; Kafka:
    lag)."""
    oldest_age_seconds: float | None
    """Age of the oldest waiting message, or ``None`` when the broker cannot report it
    cheaply (SQS publishes it only as a CloudWatch metric, not a queue attribute)."""


class BacklogProbe(ABC):
    """A consumer capability: report a topic's backlog."""

    @abstractmethod
    async def backlog(self, topic: str) -> Backlog:
        """Return the work currently waiting on ``topic``.

        Args:
            topic: The topic or queue to inspect.

        Returns:
            The messages waiting and in flight on ``topic``.

        """
        ...
