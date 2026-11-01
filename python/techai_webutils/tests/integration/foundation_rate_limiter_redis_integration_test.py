"""Integration test: the Redis per-key rate limiter enforces one limit across instances."""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest
import pytest_asyncio
from redis.asyncio import Redis

from techai_webutils.foundation.resilience.per_key_builder import (
    PerKeyRateLimiterConfig,
    PerKeyRateLimiterKind,
    per_key_rate_limiter_from_config,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


@pytest_asyncio.fixture
async def redis_pair(redis_container: object) -> AsyncIterator[tuple[Redis, Redis]]:
    """Yield two independent clients (two "replicas") on the session's Redis."""
    host = redis_container.get_container_host_ip()  # type: ignore[attr-defined]
    port = int(redis_container.get_exposed_port(redis_container.port))  # type: ignore[attr-defined]
    first, second = Redis(host=host, port=port), Redis(host=host, port=port)
    try:
        yield first, second
    finally:
        await first.aclose()
        await second.aclose()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_two_limiters_share_one_window(
    redis_pair: tuple[Redis, Redis], request: pytest.FixtureRequest
) -> None:
    """Test that two limiter instances on one Redis share a single allowance.

    **Why this test is important:**
      - A per-replica limit multiplies by the replica count; the shared kind exists to stop that.

    **What it tests:**
      - with a limit of 5, ten calls alternating between two instances allow exactly five
      - a different key has its own allowance
    """
    config = PerKeyRateLimiterConfig(
        kind=PerKeyRateLimiterKind.REDIS,
        max_per_window=5,
        window_seconds=3600,
        prefix=f"rl-{request.node.name}",
    )
    limiters = [
        per_key_rate_limiter_from_config(config, redis=client) for client in redis_pair
    ]

    allowed = [await limiters[i % 2].allow_key("u-1") for i in range(10)]
    other = await limiters[1].allow_key("u-2")

    assert sum(allowed) == 5
    assert other
