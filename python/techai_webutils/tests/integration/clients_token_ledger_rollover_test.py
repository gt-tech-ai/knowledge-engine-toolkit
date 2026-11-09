"""Integration tests.

Budget rollover at the window boundary and enforcement under a Redis outage.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from decimal import Decimal
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest
import pytest_asyncio
from redis.asyncio import Redis
from testcontainers.redis import RedisContainer

from techai_webutils.clients.decorators.token_accounting import TokenAccountingProxy
from techai_webutils.clients.token_ledger import TokenLedgerConfig, TokenLedgerKind
from techai_webutils.clients.token_ledger.redis import RedisTokenLedger
from techai_webutils.core.errors import QuotaExceededError
from techai_webutils.core.interfaces.llm import LLMMessage, LLMProvider, LLMResponse
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.token_ledger import Period, UsageRecord, UsageScope
from techai_webutils.foundation.tokens.cost import ModelPrice, PriceTable
from techai_webutils.foundation.tokens.policy import BudgetPolicy, Enforcement

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable

_SCOPE = UsageScope(org_id="org-1", workspace_id="w-1")
_TOMORROW = datetime.now(UTC).date() + timedelta(days=1)
_LAST_SECOND = datetime(
    _TOMORROW.year, _TOMORROW.month, _TOMORROW.day, 23, 59, 59, tzinfo=UTC
)
"""The last second of tomorrow (UTC): its counter expires in the future by the server's
clock, which really runs (a past day's counter is written already expired)."""
_NEXT_DAY = _LAST_SECOND + timedelta(seconds=2)
_PRICES = PriceTable(
    prices={"m": ModelPrice(Decimal(1), Decimal(1), Decimal(0))}, version="v1"
)


def _client(container: object) -> Redis:
    """Return a client for ``container`` that fails fast once it is gone.

    Returns:
        A client for ``container`` that fails fast once it is gone.

    """
    host = container.get_container_host_ip()  # type: ignore[attr-defined]
    port = int(container.get_exposed_port(container.port))  # type: ignore[attr-defined]
    return Redis(host=host, port=port, socket_timeout=1, socket_connect_timeout=1)


@pytest_asyncio.fixture
async def redis_client(redis_container: object) -> AsyncIterator[Redis]:
    """Yield a client bound to the session's Redis.

    Yields:
        A client bound to the session's Redis.

    """
    client = _client(redis_container)
    try:
        yield client
    finally:
        await client.aclose()


def _ledger(
    client: Redis,
    prefix: str,
    clock: Callable[[], datetime],
    metrics: MetricsProvider | None = None,
) -> RedisTokenLedger:
    """Return a daily-window ledger under ``prefix`` reading the window at ``clock``.

    Returns:
        A daily-window ledger under ``prefix`` reading the window at ``clock``.

    """
    return RedisTokenLedger(
        client,
        TokenLedgerConfig(
            kind=TokenLedgerKind.REDIS,
            window=Period.DAILY,
            redis_prefix=prefix,
            stream=f"{prefix}:s",
        ),
        metrics=metrics,
        clock=clock,
    )


def _usage(ts: datetime, tokens: int) -> UsageRecord:
    """Return a usage of ``tokens`` input tokens for ``_SCOPE`` at ``ts``.

    Returns:
        A usage of ``tokens`` input tokens for ``_SCOPE`` at ``ts``.

    """
    return UsageRecord(
        scope=_SCOPE,
        model="m",
        operation="generate",
        input_tokens=tokens,
        output_tokens=0,
        ts=ts,
    )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_exhausted_budget_is_served_again_after_rollover(
    redis_client: Redis, request: pytest.FixtureRequest
) -> None:
    """Test that a daily budget exhausted at 23:59:59 allows again at 00:00:01.

    **Why this test is important:**
      - ``RESOURCE_EXHAUSTED`` promises the quota resets; a counter that never rolls over
        blocks a tenant forever.

    **What it tests:**
      - 120 tokens against a 100-token daily limit → exhausted in the same day
      - the next day's first second → allowed, with the full allowance remaining
    """
    prefix = f"tl-{request.node.name}"
    await redis_client.hset(f"{prefix}:limits:org-1:w-1", mapping={"tokens": "100"})
    now = {"t": _LAST_SECOND}
    ledger = _ledger(redis_client, prefix, lambda: now["t"])

    await ledger.record(_usage(_LAST_SECOND, 120))
    before = await ledger.check_budget(_SCOPE)
    now["t"] = _NEXT_DAY
    after = await ledger.check_budget(_SCOPE)

    assert (before.allowed, before.reason) == (False, "budget_exhausted")
    assert (after.allowed, after.remaining_tokens) == (True, 100)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_redis_outage_soft_serves_hard_rejects_and_counts_lost_records() -> None:
    """Test enforcement when the ledger's Redis goes away mid-flight.

    **Why this test is important:**
      - The default (soft) must keep answering through a FinOps outage; a hard cap must
        not turn the outage into free spend; and the lost records must be visible, not
        silent.

    **What it tests:**
      - with Redis stopped, a ``soft`` proxy serves and a ``hard`` proxy raises
        ``QuotaExceededError`` before the model call
      - each record attempt under the outage increments
        ``token_ledger_record_failed_total`` and never raises
    """
    failed = MagicMock()
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(return_value=failed)
    inner = MagicMock(spec=LLMProvider)
    inner.complete = AsyncMock(
        return_value=LLMResponse(
            content="a", model="m", input_tokens=1, output_tokens=1, finish_reason="stop"
        )
    )
    container = RedisContainer("redis:7-alpine")
    container.start()
    client = _client(container)
    ledger = _ledger(client, "tl-outage", lambda: _LAST_SECOND, metrics)

    def proxy(enforcement: Enforcement) -> LLMProvider:
        return cast(
            "LLMProvider",
            TokenAccountingProxy(
                inner,
                ledger=ledger,
                scope=lambda: _SCOPE,
                prices=_PRICES,
                policy=BudgetPolicy(enforcement=enforcement),
                step="generate",
            ),
        )

    try:
        container.stop()
        served = await proxy(Enforcement.SOFT).complete([
            LLMMessage(role="user", content="q")
        ])
        with pytest.raises(QuotaExceededError):
            await proxy(Enforcement.HARD).complete([LLMMessage(role="user", content="q")])
    finally:
        await client.aclose()

    assert served.content == "a"
    inner.complete.assert_awaited_once()
    failed.inc.assert_called_once()
