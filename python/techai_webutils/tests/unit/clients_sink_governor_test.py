"""Unit tests for ``SinkWriteGovernor``.

Sink writes paced by the readers' query latency.
"""

from __future__ import annotations

import asyncio
from dataclasses import replace
from typing import cast, final
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.sink import GovernorConfig, SinkWriteGovernor
from techai_webutils.core.errors import AppValueError, UnavailableError
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.metrics_query import MetricsQuerier
from techai_webutils.core.interfaces.sink import ChunkPayload, Sink, SinkDocument

_EXPECTED_OVER = 4
_EXPECTED_UNDER = 5
_EXPECTED_AWAIT_COUNT = 2
_EXPECTED_PEAK = 8

_DOCUMENT = SinkDocument(
    org_id="o1",
    document_id="d",
    content_hash="h",
    embedding_model="m",
    embedding_version="1",
)
"""Any document; the governor never looks inside one."""

_CONFIG = GovernorConfig(
    query="p99_ms", max_query_p99_ms=500.0, max_concurrency=8, check_seconds=10.0
)
"""Eight writes at most, halved while p99 exceeds 500 ms, checked every 10 s."""


@final
class _Clock:
    """A monotonic clock the test advances by hand."""

    def __init__(self) -> None:
        """Start at 100 s, so the first write is already due a check."""
        self.now = 100.0

    def __call__(self) -> float:
        """Return the current time.

        Returns:
            The current time.

        """
        return self.now


async def _peak_writes(inner: MagicMock, governor: Sink, count: int) -> int:
    """Start ``count`` writes at once, let them reach ``inner``.

    And return the peak in flight.

    Returns:
        The peak concurrent writes.

    """
    active, peak, gate = 0, 0, asyncio.Event()

    async def write(*_args: object) -> None:
        nonlocal active, peak
        active += 1
        peak = max(peak, active)
        _ = await gate.wait()
        active -= 1

    cast("MagicMock", inner.write).side_effect = write
    tasks = [
        asyncio.create_task(governor.write(_DOCUMENT, [ChunkPayload(text="t")], [[0.0]]))
        for _ in range(count)
    ]
    for _ in range(20):
        await asyncio.sleep(0)
    gate.set()
    _ = await asyncio.gather(*tasks)
    return peak


@pytest.mark.asyncio
async def test_bulk_writes_back_off_when_query_p99_exceeds_budget() -> None:
    """Test that writes halve their concurrency over budget and recover by one below it.

    **Why this test is important:**
      - Ingestion and retrieval share the domain; unthrottled bulk writes during a
        backfill push the readers' p99 past their SLO, so ingestion must yield fast and
        come back slowly.

    **What it tests:**
      - With p99 900 ms over a 500 ms budget, eight concurrent writes run at most four at
        a time; at the next check, with p99 100 ms, they run at most five at a time.
    """
    querier = MagicMock(spec=MetricsQuerier)
    querier.query = AsyncMock(side_effect=[900.0, 100.0])
    clock, inner = _Clock(), AsyncMock(spec=Sink)
    governor = SinkWriteGovernor(inner, querier, _CONFIG, clock=clock)

    over = await _peak_writes(inner, governor, 8)
    clock.now += 10.0
    under = await _peak_writes(inner, governor, 8)

    assert over == _EXPECTED_OVER
    assert under == _EXPECTED_UNDER
    assert (cast("AsyncMock", querier.query).await_count) == _EXPECTED_AWAIT_COUNT
    cast("MagicMock", cast("AsyncMock", querier.query).assert_awaited_with)("p99_ms")


@pytest.mark.asyncio
async def test_governor_holds_concurrency_when_metrics_read_fails() -> None:
    """Test that a failed latency read keeps the current concurrency and is counted.

    **Why this test is important:**
      - A Prometheus outage must neither stop ingestion nor unthrottle it; the governor
        holds its last decision, and the failure is visible on a dashboard.

    **What it tests:**
      - A querier that raises leaves eight writes running eight at a time, does not fail
        the writes, and counts one ``sink_write_governor_read_failures_total``.
    """
    querier = MagicMock(spec=MetricsQuerier)
    querier.query = AsyncMock(side_effect=UnavailableError("prometheus down"))
    metrics, inner = MagicMock(spec=MetricsProvider), AsyncMock(spec=Sink)
    governor = SinkWriteGovernor(inner, querier, _CONFIG, metrics=metrics, clock=_Clock())

    peak = await _peak_writes(inner, governor, 8)

    assert peak == _EXPECTED_PEAK
    cast("MagicMock", cast("MagicMock", metrics.counter).assert_any_call)(
        "sink_write_governor_read_failures_total",
        "Query-latency reads the sink write governor failed and held through.",
        [],
    )
    cast(
        "MagicMock",
        cast(
            "MagicMock",
            cast("MagicMock", cast("MagicMock", metrics.counter).return_value).inc,
        ).assert_called_once_with,
    )()


@pytest.mark.parametrize(
    "config",
    [
        replace(_CONFIG, min_concurrency=0),
        replace(_CONFIG, max_concurrency=1, min_concurrency=2),
        replace(_CONFIG, max_query_p99_ms=0.0),
        replace(_CONFIG, check_seconds=0.0),
        replace(_CONFIG, query=""),
    ],
)
def test_governor_refuses_an_unusable_config(config: GovernorConfig) -> None:
    """Test that a governor that could never write, or never decide, is refused at wiring.

    **Why this test is important:**
      - A zero concurrency floor would stall every write; a non-positive budget would
        halve concurrency forever; an empty query has nothing to read.

    **What it tests:**
      - A floor of 0, a ceiling below the floor, a non-positive budget or interval, and an
        empty query each raise ``AppValueError``.
    """
    with pytest.raises(AppValueError):
        _ = SinkWriteGovernor(
            AsyncMock(spec=Sink), MagicMock(spec=MetricsQuerier), config
        )
