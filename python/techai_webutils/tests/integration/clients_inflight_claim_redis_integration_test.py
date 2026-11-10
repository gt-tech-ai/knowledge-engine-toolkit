"""Integration tests for ``RedisInflightClaim`` against a real Redis server."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast

import pytest
import pytest_asyncio
from redis.asyncio import Redis

from techai_webutils.clients.inflight_claim import (
    InflightClaimConfig,
    InflightClaimKind,
    inflight_claim_from_config,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from testcontainers.redis import RedisContainer

pytestmark = pytest.mark.integration


@pytest_asyncio.fixture
async def redis_client(redis_container: object) -> AsyncIterator[Redis]:
    """Yield an async client on the session's Redis container, flushed first.

    Args:
        redis_container: A real Redis server for the session.

    Yields:
        An async client on the session's Redis container, flushed first.

    """
    container = cast("RedisContainer", redis_container)
    host = container.get_container_host_ip()
    port = int(container.get_exposed_port(container.port))
    client = Redis(host=host, port=port)
    _ = await client.flushdb()
    try:
        yield client
    finally:
        await client.aclose()


@pytest.mark.asyncio
async def test_concurrent_claims_one_winner(redis_client: Redis) -> None:
    """Test that two consumers claiming the same key at once get exactly one win.

    **Why this test is important:**
      - This is the whole point of the claim: a duplicate delivery runs the costly stage
        once.

    **What it tests:**
      - two claims on separate instances, gathered concurrently, return one True and one
        False
      - after the winner releases, a third claim wins

    Args:
        redis_client: An async client on the session's Redis container, flushed first.

    """
    config = InflightClaimConfig(kind=InflightClaimKind.REDIS)
    first = inflight_claim_from_config(config, redis=redis_client)
    second = inflight_claim_from_config(config, redis=redis_client)

    results = await asyncio.gather(first.claim("k", 30), second.claim("k", 30))
    assert sorted(results) == [False, True]

    winner = first if results[0] else second
    await winner.release("k")
    assert await inflight_claim_from_config(config, redis=redis_client).claim("k", 30)


@pytest.mark.asyncio
async def test_claim_lapses_and_stale_release_keeps_new_holder(
    redis_client: Redis,
) -> None:
    """Test that a claim expires after its TTL.

    Its late release leaves the next holder's claim.

    **Why this test is important:**
      - A consumer that died holding a claim must not block the work; one that ran past
        its TTL must not free the claim another consumer has since taken.

    **What it tests:**
      - after the 0.2 s TTL a second consumer wins the key
      - the first consumer's release leaves the key in place (a third claim loses)

    Args:
        redis_client: An async client on the session's Redis container, flushed first.

    """
    config = InflightClaimConfig(kind=InflightClaimKind.REDIS, prefix="t")
    first = inflight_claim_from_config(config, redis=redis_client)
    second = inflight_claim_from_config(config, redis=redis_client)
    assert await first.claim("k", 0.2)

    await asyncio.sleep(0.4)
    assert await second.claim("k", 30)
    await first.release("k")

    assert await redis_client.exists("t:k") == 1
    assert not await inflight_claim_from_config(config, redis=redis_client).claim("k", 30)
