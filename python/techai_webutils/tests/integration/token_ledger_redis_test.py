"""Integration tests for ``RedisTokenLedger`` against a real Redis 7 (testcontainers).

The unit suite mocks the client; this suite proves what only a real server can: the Lua script's
atomic counters and expiry, the stream entry the durable ledger drains, and ``MAXLEN ~`` trimming.
"""

from __future__ import annotations

from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING

import pytest
import pytest_asyncio
from redis.asyncio import Redis

from techai_webutils.clients.token_ledger import (
    TokenLedgerConfig,
    TokenLedgerKind,
    token_ledger_from_config,
)
from techai_webutils.core.interfaces.token_ledger import UsageRecord, UsageScope

if TYPE_CHECKING:
    from collections.abc import AsyncIterator


@pytest_asyncio.fixture
async def redis_client(redis_container: object) -> AsyncIterator[Redis]:
    """Yield a ``decode_responses`` async client bound to the session's Redis container."""
    host = redis_container.get_container_host_ip()  # type: ignore[attr-defined]
    port = int(redis_container.get_exposed_port(redis_container.port))  # type: ignore[attr-defined]
    client = Redis(host=host, port=port, decode_responses=True)
    try:
        yield client
    finally:
        await client.aclose()


def _record(
    ts: datetime, *, input_tokens: int = 100, output_tokens: int = 20
) -> UsageRecord:
    """One generation for org-1 / w-1 at ``ts``."""
    return UsageRecord(
        scope=UsageScope(
            org_id="org-1", team_id="t-1", workspace_id="w-1", user_id="u-1"
        ),
        model="nova-lite",
        operation="generate",
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        cost_usd=Decimal("0.010800"),
        pricing_version="2026-10",
        trace_id="4bf92f3577b34da6a3ce929d0e0e4736",
        ts=ts,
    )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_counter_increments_and_expires_at_window(
    redis_client: Redis, request: pytest.FixtureRequest
) -> None:
    """Test that recorded usage accumulates in the window hash, which expires at the month's end.

    **Why this test is important:**
      - Budgets are checked against these counters; they must sum exactly and roll over at the
        window boundary, or an org is blocked forever or never.

    **What it tests:**
      - two records sum to exactly ``{input 300, output 40, embed 0, cost 21600 micro-dollars}``
      - the hash's absolute expiry (``EXPIRETIME``) is the first instant of next UTC month
    """
    prefix = f"tl-{request.node.name}"
    ledger = token_ledger_from_config(
        TokenLedgerConfig(
            kind=TokenLedgerKind.REDIS, redis_prefix=prefix, stream=f"{prefix}:usage"
        ),
        redis=redis_client,
    )
    now = datetime.now(UTC)

    await ledger.record(_record(now))
    await ledger.record(_record(now, input_tokens=200, output_tokens=20))

    key = f"{prefix}:org-1:w-1:{now.strftime('%Y-%m')}"
    next_month = datetime(now.year + (now.month == 12), now.month % 12 + 1, 1, tzinfo=UTC)
    assert await redis_client.hgetall(key) == {
        "input_tokens": "300",
        "output_tokens": "40",
        "embed_tokens": "0",
        "cost_micro_usd": "21600",
    }
    assert await redis_client.expiretime(key) == int(next_month.timestamp())


@pytest.mark.integration
@pytest.mark.asyncio
async def test_xadd_entry_reads_back_with_contract_fields(
    redis_client: Redis, request: pytest.FixtureRequest
) -> None:
    """Test that each record appends one stream entry carrying exactly the contract fields.

    **Why this test is important:**
      - The durable ledger's drainer parses these entries; a missing or renamed field loses usage.

    **What it tests:**
      - one ``XADD`` per record, whose fields equal the contract mapping value-for-value
    """
    prefix = f"tl-{request.node.name}"
    stream = f"{prefix}:usage"
    ledger = token_ledger_from_config(
        TokenLedgerConfig(kind=TokenLedgerKind.REDIS, redis_prefix=prefix, stream=stream),
        redis=redis_client,
    )
    ts = datetime.now(UTC).replace(microsecond=0)

    await ledger.record(_record(ts))

    entries = await redis_client.xrange(stream)
    assert entries is not None
    assert len(entries) == 1
    assert entries[0][1] == {
        "org_id": "org-1",
        "team_id": "t-1",
        "workspace_id": "w-1",
        "user_id": "u-1",
        "model": "nova-lite",
        "operation": "generate",
        "input_tokens": "100",
        "output_tokens": "20",
        "embed_tokens": "0",
        "cost_usd": "0.010800",
        "pricing_version": "2026-10",
        "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
        "ts": ts.isoformat().replace("+00:00", "Z"),
    }


@pytest.mark.integration
@pytest.mark.asyncio
async def test_stream_is_trimmed_to_maxlen(
    redis_client: Redis, request: pytest.FixtureRequest
) -> None:
    """Test that the usage stream stays bounded by the approximate ``MAXLEN``.

    **Why this test is important:**
      - If the drainer stalls, an unbounded stream would grow until Redis runs out of memory.

    **What it tests:**
      - after 1000 records with ``stream_maxlen=100`` the stream holds at most 100 entries plus
        one radix-tree node (Redis trims ``~`` at node granularity; default 100 entries per node)
    """
    prefix = f"tl-{request.node.name}"
    stream = f"{prefix}:usage"
    ledger = token_ledger_from_config(
        TokenLedgerConfig(
            kind=TokenLedgerKind.REDIS,
            redis_prefix=prefix,
            stream=stream,
            stream_maxlen=100,
        ),
        redis=redis_client,
    )
    now = datetime.now(UTC)

    for _ in range(1000):
        await ledger.record(_record(now))

    length = await redis_client.xlen(stream)
    assert 100 <= length <= 200
