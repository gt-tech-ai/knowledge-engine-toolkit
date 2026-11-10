"""Publishes a ``BacklogProbe``'s readings as the broker-neutral backlog gauges."""

from __future__ import annotations

from typing import TYPE_CHECKING, final

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.messaging_backlog import Backlog, BacklogProbe
    from techai_webutils.core.interfaces.metrics import MetricsProvider


@final
class BacklogGauges:
    """The backlog and oldest-age messaging gauges.

    ``messaging_backlog_messages{topic}`` and ``messaging_oldest_age_seconds{topic}``.
    """

    def __init__(self, metrics: MetricsProvider) -> None:
        """Register both gauges on ``metrics``.

        Args:
            metrics: Where the gauges are registered.

        """
        self._messages = metrics.gauge(
            "messaging_backlog_messages",
            "Messages waiting to be received, by topic.",
            ["topic"],
        )
        self._oldest = metrics.gauge(
            "messaging_oldest_age_seconds",
            "Age of the oldest waiting message in seconds, by topic.",
            ["topic"],
        )

    async def report(self, probe: BacklogProbe, topic: str) -> Backlog:
        """Read ``topic``'s backlog from ``probe`` and set the gauges.

        Returns the reading.

        The age gauge is left unchanged when the broker cannot report an age.

        Args:
            probe: Reads a topic's backlog.
            topic: The topic.

        Returns:
            The backlog read from the probe.

        """
        backlog = await probe.backlog(topic)
        self._messages.set(backlog.messages, topic=topic)
        if backlog.oldest_age_seconds is not None:
            self._oldest.set(backlog.oldest_age_seconds, topic=topic)
        return backlog
