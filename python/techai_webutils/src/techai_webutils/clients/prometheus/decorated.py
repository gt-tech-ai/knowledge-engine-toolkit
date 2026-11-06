"""Runs a metrics querier's reads through the client tier.

``new_metrics_querier_from_config`` returns an undecorated ``MetricsQuerier``; ``decorate`` gives it
a ``ClientBase``, so each read carries the timeout, retry, breaker and observability the consumer's
``decoration.clients`` configures for the querier's name. Reads have no side effects, so each may
be retried when the config turns retry on.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.interfaces.metrics_query import MetricsQuerier

if TYPE_CHECKING:
    from datetime import datetime, timedelta

    from techai_webutils.core.interfaces.metrics_query import MetricSample
    from techai_webutils.foundation.decorate import Decorations


def decorate(
    querier: MetricsQuerier, decorations: Decorations, name: str
) -> MetricsQuerier:
    """Return ``querier`` with every read run through the client tier as the client ``name``.

    Args:
        querier: The querier to wrap.
        decorations: The decorators the client tier wraps each read in.
        name: The client name (its ``decoration.clients`` key).

    Returns:
        The decorated querier.

    """
    return _DecoratedQuerier(querier, ClientBase(MetricsQuerier, decorations, name))


class _DecoratedQuerier(MetricsQuerier):
    """A metrics querier whose reads run through a ClientBase."""

    def __init__(self, inner: MetricsQuerier, client: ClientBase) -> None:
        """Bind the wrapped querier and the client tier it runs through.

        Args:
            inner: The wrapped querier.
            client: The client tier it runs through.

        """
        self._inner = inner
        self._client = client

    @override
    async def query(self, query: str) -> float:
        """Evaluate an instant query through the tier.

        Args:
            query: The PromQL.

        Returns:
            The single sample value.

        """
        return await self._client.exec("query", lambda: self._inner.query(query))

    @override
    async def query_range(
        self, query: str, start: datetime, end: datetime, step: timedelta
    ) -> list[MetricSample]:
        """Evaluate a range query through the tier.

        Args:
            query: The PromQL.
            start: The first evaluation time.
            end: The last evaluation time.
            step: The evaluation step.

        Returns:
            The query's one series.

        """
        return await self._client.exec(
            "query_range", lambda: self._inner.query_range(query, start, end, step)
        )
