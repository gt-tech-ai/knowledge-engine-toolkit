"""Unit tests for the distinct-counter factory and the redis counter's failure path."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest
from redis.asyncio import Redis
from redis.exceptions import ConnectionError as RedisConnectionError

from techai_webutils.clients.distinct_counter import (
    DistinctCounterConfig,
    DistinctCounterKind,
    distinct_counter_from_config,
)
from techai_webutils.core.errors import AppValueError


@pytest.mark.asyncio
async def test_noop_counts_nothing_and_redis_without_a_host_is_refused() -> None:
    """Test that the default counter does no work and the redis kind needs somewhere to count.

    **Why this test is important:**
      - The noop kind is the zero-cost default, so it must answer without any store; a redis
        kind built with no client and no host would fail on the first document instead of at
        start-up.

    **What it tests:**
      - the default config's counter returns a zero per key
      - the redis kind with neither a client nor a host raises ``AppValueError``
    """
    counter = distinct_counter_from_config(DistinctCounterConfig())
    assert await counter.observe(["a", "b"], "d1") == [0, 0]

    with pytest.raises(AppValueError):
        distinct_counter_from_config(
            DistinctCounterConfig(kind=DistinctCounterKind.REDIS)
        )


@pytest.mark.asyncio
async def test_redis_error_returns_zero_counts_and_is_counted() -> None:
    """Test that a Redis outage leaves the counts at zero rather than failing the caller.

    **Why this test is important:**
      - The counts are advisory (a boilerplate mark); a Redis outage must not stop ingestion.

    **What it tests:**
      - a pipeline whose execute raises a connection error yields zeros for every key
      - the error is counted once in the injected error counter, labelled ``observe``
    """
    pipeline = MagicMock()
    pipeline.execute = AsyncMock(side_effect=RedisConnectionError("down"))
    redis = MagicMock(spec=Redis)
    redis.pipeline.return_value = pipeline
    errors = MagicMock()
    metrics = MagicMock()
    metrics.counter.return_value = errors

    counter = distinct_counter_from_config(
        DistinctCounterConfig(kind=DistinctCounterKind.REDIS),
        redis=redis,
        metrics=metrics,
    )

    assert await counter.observe(["a", "b"], "d1") == [0, 0]
    errors.inc.assert_called_once_with(op="observe")
