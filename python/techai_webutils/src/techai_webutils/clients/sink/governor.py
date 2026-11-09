"""``SinkWriteGovernor``: pace a sink's writes by the readers' query latency (AIMD).

Ingestion and retrieval share a search domain, so a backfill's bulk writes can push the
readers' latency past their SLO. The governor caps how many sink calls run at once. At
most once per ``check_seconds`` it reads the readers' p99 through a ``MetricsQuerier``:
over ``max_query_p99_ms`` it halves the cap (never below ``min_concurrency``), under it
the cap grows by one (never above ``max_concurrency``). Back off fast, recover slowly. A
failed read holds the cap and is counted (``sink_write_governor_read_failures_total``); it
never fails a write. The check runs under a lock, so the calls that arrive while it is in
flight wait for its decision. The cap is exported as ``sink_write_governor_concurrency``.
"""

from __future__ import annotations

import asyncio
import time
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sink import Sink
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator, Callable, Mapping, Sequence

    from techai_webutils.core.interfaces.metrics import (
        MetricCounter,
        MetricGauge,
        MetricsProvider,
    )
    from techai_webutils.core.interfaces.metrics_query import MetricsQuerier
    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument


@dataclass(frozen=True, slots=True)
class GovernorConfig:
    """The latency budget the governor holds the readers to.

    And its concurrency bounds.
    """

    query: str = ""
    """PromQL returning the readers' current p99 latency, in milliseconds; required (the
    empty default is refused when the governor is built)."""
    max_query_p99_ms: float = 0.0
    """The p99 above which the governor halves the sink's concurrency; required (the zero
    default is refused when the governor is built)."""
    max_concurrency: int = 8
    """The cap the governor starts at and never exceeds."""
    min_concurrency: int = 1
    """The floor the cap never falls below, so ingestion always makes progress."""
    check_seconds: float = 15.0
    """The least time between two latency reads."""


class SinkWriteGovernor(DelegatingAsyncResource[Sink], Sink):
    """A sink whose calls run under an AIMD concurrency cap driven by the readers' p99."""

    def __init__(
        self,
        inner: Sink,
        querier: MetricsQuerier,
        config: GovernorConfig,
        *,
        metrics: MetricsProvider | None = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        """Govern ``inner`` by the p99 ``querier`` reads for ``config.query``.

        Raises:
            AppValueError: An empty query, a floor below 1, a ceiling below the floor, or
                a non-positive budget or interval (``INVALID_INPUT``).

        """
        _validate(config)
        self._inner = inner
        self._querier = querier
        self._config = config
        self._clock = clock
        self._limit = config.max_concurrency
        self._active = 0
        self._slots = asyncio.Condition()
        self._check = asyncio.Lock()
        self._next_check = float("-inf")
        self._logger = get_logger("sink_write_governor")
        self._failures: MetricCounter | None = None
        self._gauge: MetricGauge | None = None
        if metrics is not None:
            self._failures = metrics.counter(
                "sink_write_governor_read_failures_total",
                "Query-latency reads the sink write governor failed and held through.",
                [],
            )
            self._gauge = metrics.gauge(
                "sink_write_governor_concurrency",
                "The sink calls the write governor lets run at once.",
                [],
            )
            self._gauge.set(self._limit)

    @override
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Write under the cap."""
        async with self._slot():
            await self._inner.write(document, chunks, vectors)

    @override
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete under the cap."""
        async with self._slot():
            await self._inner.delete(org_id, document_id)

    @override
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Update metadata under the cap."""
        async with self._slot():
            await self._inner.update_metadata(org_id, document_id, fields)

    @asynccontextmanager
    async def _slot(self) -> AsyncGenerator[None]:
        """Re-check the latency when due.

        Then hold one of the cap's slots for the call.
        """
        async with self._check:
            if self._clock() >= self._next_check:
                self._next_check = self._clock() + self._config.check_seconds
                await self._adjust()
        async with self._slots:
            await self._slots.wait_for(lambda: self._active < self._limit)
            self._active += 1
        try:
            yield
        finally:
            async with self._slots:
                self._active -= 1
                self._slots.notify_all()

    async def _adjust(self) -> None:
        """Halve the cap over budget, grow it by one under budget.

        Hold it when the read fails.
        """
        cfg = self._config
        try:
            p99 = await self._querier.query(cfg.query)
        except Exception as exc:
            self._logger.warning(
                "sink write governor held its cap", cap=self._limit, exc_info=exc
            )
            if self._failures is not None:
                self._failures.inc()
            return
        if p99 > cfg.max_query_p99_ms:
            limit = max(cfg.min_concurrency, self._limit // 2)
        else:
            limit = min(cfg.max_concurrency, self._limit + 1)
        async with self._slots:
            self._limit = limit
            self._slots.notify_all()
        if self._gauge is not None:
            self._gauge.set(limit)


def _validate(config: GovernorConfig) -> None:
    """Refuse a config under which the governor could never write or never decide.

    Raises:
        AppValueError: The reason (``INVALID_INPUT``).

    """
    if not config.query:
        msg = "sink governor: a latency query is required"
        raise AppValueError(msg)
    if config.min_concurrency < 1 or config.max_concurrency < config.min_concurrency:
        msg = (
            "sink governor: need 1 <= min_concurrency <= max_concurrency, got "
            f"{config.min_concurrency} and {config.max_concurrency}"
        )
        raise AppValueError(msg)
    if config.max_query_p99_ms <= 0 or config.check_seconds <= 0:
        msg = "sink governor: max_query_p99_ms and check_seconds must be positive"
        raise AppValueError(msg)
