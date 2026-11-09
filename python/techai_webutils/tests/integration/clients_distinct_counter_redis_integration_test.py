"""Integration tests for the ``redis`` ``DistinctCounter`` against a real Redis server."""

from __future__ import annotations

from typing import TYPE_CHECKING, cast

import pytest
import pytest_asyncio
from redis.asyncio import Redis

from techai_webutils.clients.distinct_counter import (
    DistinctCounterConfig,
    DistinctCounterKind,
    distinct_counter_from_config,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from testcontainers.redis import RedisContainer

pytestmark = pytest.mark.integration


@pytest_asyncio.fixture
async def redis_client(redis_container: object) -> AsyncIterator[Redis]:
    """Yield an async client on the session's Redis container, flushed first.

    Yields:
        An async client on the session's Redis container, flushed first.

    """
    container = cast("RedisContainer", redis_container)
    host = container.get_container_host_ip()
    port = int(container.get_exposed_port(container.port))
    client = Redis(host=host, port=port)
    await client.flushdb()
    try:
        yield client
    finally:
        await client.aclose()


@pytest.mark.asyncio
async def test_redis_counter_counts_each_member_once_and_expires_its_keys(
    redis_client: Redis,
) -> None:
    """Test that the redis counter counts distinct members per key and sets the retention.

    **Why this test is important:**
      - Boilerplate marking counts the documents that carried a block; a redelivered
        document counted twice would mark a block boilerplate too early, and a key with no
        expiry would grow Redis without bound.

    **What it tests:**
      - three members observed under "a" (one of them twice) and one under "b" count 3 and
        1
      - the keys live under the prefix with a TTL no longer than the configured retention
    """
    config = DistinctCounterConfig(
        kind=DistinctCounterKind.REDIS, prefix="bp", ttl_seconds=600
    )
    counter = distinct_counter_from_config(config, redis=redis_client)

    for member in ("d1", "d2", "d1"):
        await counter.observe(["a"], member)
    counts = await counter.observe(["a", "b"], "d3")

    assert counts == [3, 1]
    ttl = await redis_client.ttl("bp:a")
    assert 0 < ttl <= 600
