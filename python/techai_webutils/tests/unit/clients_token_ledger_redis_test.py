"""Unit tests for the Redis hot-path ``TokenLedger``.

(the async Redis client is mocked).
"""

from __future__ import annotations

from dataclasses import replace
from datetime import UTC, datetime
from decimal import Decimal
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from redis.asyncio import Redis
from redis.asyncio.client import Pipeline
from redis.exceptions import ConnectionError as RedisConnectionError

from techai_webutils.clients.token_ledger import (
    TokenLedgerConfig,
    TokenLedgerKind,
    token_ledger_from_config,
)
from techai_webutils.clients.token_ledger.redis import RedisTokenLedger
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    Enforcement,
    Period,
    UsageRecord,
    UsageScope,
    UsageSummary,
)

_EXPECTED_KEYS_COUNT = 30
_EXPECTED_KWARGS = 14
_EXPECTED_CALL_ARGS_LIST_COUNT = 30

_NOW = datetime(2026, 10, 9, 15, 30, tzinfo=UTC)
_SCOPE = UsageScope(org_id="org-1", team_id="t-1", workspace_id="w-1", user_id="u-1")


def _record() -> UsageRecord:
    """One costed generation for ``_SCOPE`` at ``_NOW``.

    Returns:
        The usage record.

    """
    return UsageRecord(
        scope=_SCOPE,
        model="nova-lite",
        operation="generate",
        input_tokens=100,
        output_tokens=20,
        embed_tokens=0,
        cost_usd=Decimal("0.010800"),
        pricing_version="2026-10",
        trace_id="4bf92f3577b34da6a3ce929d0e0e4736",
        ts=_NOW,
    )


def _client() -> tuple[MagicMock, AsyncMock]:
    """Return a mocked async Redis client.

    And the registered record script it hands out.

    Returns:
        A mocked async Redis client.

    """
    script = AsyncMock(return_value=b"1-0")
    client = MagicMock(spec=Redis)
    cast("MagicMock", client.register_script).return_value = script
    return client, script


def _pipeline(client: MagicMock, results: list[object]) -> MagicMock:
    """Attach a non-transactional pipeline mock.

    To ``client`` whose ``execute`` returns ``results``.

    Returns:
        The pipeline mock.

    """
    pipe = MagicMock(spec=Pipeline)
    pipe.__aenter__ = AsyncMock(return_value=pipe)
    pipe.__aexit__ = AsyncMock(return_value=None)
    pipe.execute = AsyncMock(return_value=results)
    cast("MagicMock", client.pipeline).return_value = pipe
    return pipe


@pytest.mark.asyncio
async def test_record_runs_single_script_with_scope_key() -> None:
    """Test that ``record`` is one atomic script: counters.

    Window expiry and the usage stream.

    **Why this test is important:**
      - One round-trip keeps recording off the hot path's latency, and atomicity means a
        counter never moves without its stream entry (the durable ledger drains the
        stream).

    **What it tests:**
      - the factory builds ``RedisTokenLedger`` for ``kind="redis"`` and registers the
        script once
      - the script runs with keys ``[token_ledger:org-1:w-1:2026-10, token_ledger:usage]``
        and args ``[expire-at (2026-11-01 UTC), maxlen, in, out, embed, cost
        micro-dollars, …contract fields]``
    """
    client, script = _client()
    ledger = token_ledger_from_config(
        TokenLedgerConfig(kind=TokenLedgerKind.REDIS, stream_maxlen=5000), redis=client
    )

    await ledger.record(_record())

    assert isinstance(ledger, RedisTokenLedger)
    cast("MagicMock", cast("MagicMock", client.register_script).assert_called_once)()
    script.assert_awaited_once_with(
        keys=["token_ledger:org-1:w-1:2026-10", "token_ledger:usage"],
        args=[
            int(datetime(2026, 11, 1, tzinfo=UTC).timestamp()),
            5000,
            100,
            20,
            0,
            10800,
            "org_id",
            "org-1",
            "team_id",
            "t-1",
            "workspace_id",
            "w-1",
            "user_id",
            "u-1",
            "model",
            "nova-lite",
            "operation",
            "generate",
            "input_tokens",
            "100",
            "output_tokens",
            "20",
            "embed_tokens",
            "0",
            "cost_usd",
            "0.010800",
            "pricing_version",
            "2026-10",
            "trace_id",
            "4bf92f3577b34da6a3ce929d0e0e4736",
            "ts",
            "2026-10-09T15:30:00Z",
        ],
    )


@pytest.mark.asyncio
async def test_record_error_never_raises() -> None:
    """Test that a Redis failure while recording is counted and swallowed.

    **Why this test is important:**
      - Recording is best-effort on the hot path; a Redis blip must never fail a user's
        answer.

    **What it tests:**
      - with the script raising ``ConnectionError``, ``record`` returns ``None``
      - ``token_ledger_record_failed_total`` is incremented exactly once
    """
    client, script = _client()
    script.side_effect = RedisConnectionError("down")
    failed = MagicMock(spec=MetricCounter)
    metrics = MagicMock(spec=MetricsProvider)
    cast("MagicMock", metrics.counter).return_value = failed
    ledger = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), metrics=metrics
    )

    assert await ledger.record(_record()) is None

    cast("MagicMock", cast("MagicMock", metrics.counter).assert_called_once_with)(
        "token_ledger_record_failed_total",
        "Token usage records that failed to reach the ledger.",
        [],
    )
    cast("MagicMock", cast("MagicMock", failed.inc).assert_called_once_with)()


@pytest.mark.asyncio
async def test_check_budget_counter_arithmetic() -> None:
    """Test that ``check_budget`` subtracts the window's counters from the scope's limits.

    **Why this test is important:**
      - This single read decides whether a call proceeds; an off-by-one or a mixed-up unit
        lets an org overspend or blocks it early.

    **What it tests:**
      - counters (100 in, 20 out, 5 embed, 10800 micro-dollars) against limits (tokens
        1000, cost 0.05) → allowed, ``within_budget``, 875 tokens and $0.039200 left
      - counters past the token limit → not allowed, ``budget_exhausted``, 0 tokens left
      - no limits hash → allowed, ``no_limit``, both remainders ``None``
      - the reads are one pipeline over the window key and
        ``token_ledger:limits:org-1:w-1``
    """
    client, _ = _client()
    ledger = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), clock=lambda: _NOW
    )
    pipe = _pipeline(
        client,
        [[b"100", b"20", b"5", b"10800"], {b"tokens": b"1000", b"cost_usd": b"0.05"}],
    )

    within = await ledger.check_budget(_SCOPE)
    _ = _pipeline(client, [[b"900", b"100", b"0", b"0"], {b"tokens": b"1000"}])
    exhausted = await ledger.check_budget(_SCOPE)
    _ = _pipeline(client, [[None, None, None, None], {}])
    unlimited = await ledger.check_budget(_SCOPE)

    assert within == BudgetDecision(
        allowed=True,
        reason="within_budget",
        remaining_tokens=875,
        remaining_cost_usd=Decimal("0.039200"),
    )
    assert exhausted == BudgetDecision(
        allowed=False,
        reason="budget_exhausted",
        remaining_tokens=0,
        remaining_cost_usd=None,
    )
    assert unlimited == BudgetDecision(
        allowed=True, reason="no_limit", remaining_tokens=None, remaining_cost_usd=None
    )
    cast("MagicMock", cast("MagicMock", pipe.hmget).assert_called_once_with)(
        "token_ledger:org-1:w-1:2026-10",
        "input_tokens",
        "output_tokens",
        "embed_tokens",
        "cost_micro_usd",
    )
    cast("MagicMock", cast("MagicMock", pipe.hgetall).assert_called_once_with)(
        "token_ledger:limits:org-1:w-1"
    )
    cast("MagicMock", cast("MagicMock", client.pipeline).assert_called_with)(
        transaction=False
    )


@pytest.mark.asyncio
async def test_check_budget_fails_open_on_redis_error() -> None:
    """Test that an unreachable Redis allows the call with a reason naming the outage.

    **Why this test is important:**
      - The ledger must never become the reason an answer fails; enforcement degrades to
        open.

    **What it tests:**
      - with ``execute`` raising, the decision is exactly ``BudgetDecision(True,
        "ledger_unavailable_fail_open", None, None)``
    """
    client, _ = _client()
    pipe = _pipeline(client, [])
    cast("MagicMock", pipe.execute).side_effect = RedisConnectionError("down")
    ledger = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), clock=lambda: _NOW
    )

    decision = await ledger.check_budget(_SCOPE)

    assert decision == BudgetDecision(
        allowed=True,
        reason="ledger_unavailable_fail_open",
        remaining_tokens=None,
        remaining_cost_usd=None,
    )


@pytest.mark.asyncio
async def test_usage_reads_counter_snapshot() -> None:
    """Test that ``usage`` returns the window counters as an approximate summary.

    **Why this test is important:**
      - Operators read the hot-path snapshot for a quick answer; it must report the
        counters as stored, and refuse a period the counters do not cover rather than
        guess.

    **What it tests:**
      - monthly window → the summary of the current month's counters, cost converted from
        micro-dollars
      - a rolling 30-day window sums the 30 daily keys ending today
      - asking a monthly ledger for a daily period raises ``AppError(INVALID_INPUT)``
    """
    client, _ = _client()
    monthly = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), clock=lambda: _NOW
    )
    rolling = RedisTokenLedger(
        client,
        TokenLedgerConfig(kind=TokenLedgerKind.REDIS, window=Period.ROLLING_30D),
        clock=lambda: _NOW,
    )

    _ = _pipeline(client, [[b"100", b"20", b"5", b"10800"]])
    month = await monthly.usage(_SCOPE, Period.MONTHLY)
    pipe = _pipeline(client, [[b"1", b"1", b"0", b"1"]] * 30)
    rolled = await rolling.usage(_SCOPE, Period.ROLLING_30D)
    with pytest.raises(AppError) as caught:
        _ = await monthly.usage(_SCOPE, Period.DAILY)

    assert month == UsageSummary(
        scope=_SCOPE,
        period=Period.MONTHLY,
        input_tokens=100,
        output_tokens=20,
        embed_tokens=5,
        cost_usd=Decimal("0.010800"),
    )
    assert rolled == UsageSummary(
        scope=_SCOPE,
        period=Period.ROLLING_30D,
        input_tokens=30,
        output_tokens=30,
        embed_tokens=0,
        cost_usd=Decimal("0.000030"),
    )
    keys = [
        cast("list[object]", c.args)[0]
        for c in cast("list[object]", cast("MagicMock", pipe.hmget).call_args_list)
    ]
    assert keys[0] == "token_ledger:org-1:w-1:2026-10-09"
    assert keys[-1] == "token_ledger:org-1:w-1:2026-09-10"
    assert len(keys) == _EXPECTED_KEYS_COUNT
    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_check_budget_fails_open_on_malformed_limits() -> None:
    """Test that an unparsable limits hash allows the call instead of raising.

    **Why this test is important:**
      - The limits hash is written by another service; a bad value there must not turn
        every generation for that scope into an error.

    **What it tests:**
      - with ``tokens`` set to ``b"lots"`` the decision is exactly ``BudgetDecision(True,
        "ledger_unavailable_fail_open", None, None)``
    """
    client, _ = _client()
    _ = _pipeline(client, [[b"1", b"1", b"0", b"1"], {b"tokens": b"lots"}])
    ledger = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), clock=lambda: _NOW
    )

    decision = await ledger.check_budget(_SCOPE)

    assert decision == BudgetDecision(
        allowed=True,
        reason="ledger_unavailable_fail_open",
        remaining_tokens=None,
        remaining_cost_usd=None,
    )


@pytest.mark.asyncio
async def test_record_rounds_cost_half_even_and_accepts_a_string_window() -> None:
    """Test that cost is rounded half-even to micro-dollars.

    And a plain-string window is honoured.

    **Why this test is important:**
      - Truncating the cost drifts the counters below the durable ledger, which rounds
        half-even.
      - Config loaded from YAML carries the window as a string; compared by identity it
        silently fell back to daily keys, so a rolling budget read one day instead of
        thirty.

    **What it tests:**
      - ``cost_usd`` ``0.0000135`` is recorded as 14 micro-dollars (half-even), not 13
      - a ``window="rolling_30d"`` string config reads 30 daily keys in ``usage``
      - an unknown window string raises ``AppError(INVALID_INPUT)`` at construction
    """
    client, script = _client()
    rolling = RedisTokenLedger(
        client,
        TokenLedgerConfig(
            kind=TokenLedgerKind.REDIS,
            window=cast("Period", cast("object", "rolling_30d")),
        ),
        clock=lambda: _NOW,
    )

    await rolling.record(replace(_record(), cost_usd=Decimal("0.0000135")))
    pipe = _pipeline(client, [[b"0", b"0", b"0", b"0"]] * 30)
    _ = await rolling.usage(_SCOPE, Period.ROLLING_30D)
    with pytest.raises(AppError) as caught:
        _ = RedisTokenLedger(
            client,
            TokenLedgerConfig(
                kind=TokenLedgerKind.REDIS,
                window=cast("Period", cast("object", "weekly")),
            ),
        )

    assert script.await_args is not None
    assert script.await_args.kwargs["args"][5] == _EXPECTED_KWARGS
    assert (
        len(cast("list[object]", cast("MagicMock", pipe.hmget).call_args_list))
        == _EXPECTED_CALL_ARGS_LIST_COUNT
    )
    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_check_budget_reports_the_budget_enforcement() -> None:
    """Test that the limits hash's ``enforcement`` field reaches the decision.

    **Why this test is important:**
      - The limits hash is how the budget owner tells the hot path an admin's level; a
        ``per_budget`` policy enforces whatever this reads, so an unknown value must read
        as "not known" rather than as some level.

    **What it tests:**
      - ``enforcement: hard`` → ``Enforcement.HARD`` on an exhausted decision
      - a missing field → ``None``; an unknown value (``bogus``) → ``None``
      - a hash holding only ``enforcement`` (no cap) is still ``no_limit``
    """
    client, _ = _client()
    ledger = RedisTokenLedger(
        client, TokenLedgerConfig(kind=TokenLedgerKind.REDIS), clock=lambda: _NOW
    )

    async def check(limits: dict[bytes, bytes]) -> BudgetDecision:
        _ = _pipeline(client, [[b"900", b"100", b"0", b"0"], limits])
        return await ledger.check_budget(_SCOPE)

    hard = await check({b"tokens": b"1000", b"enforcement": b"hard"})
    missing = await check({b"tokens": b"1000"})
    unknown = await check({b"tokens": b"1000", b"enforcement": b"bogus"})
    uncapped = await check({b"enforcement": b"soft"})

    assert (hard.allowed, hard.enforcement) == (False, Enforcement.HARD)
    assert missing.enforcement is None
    assert unknown.enforcement is None
    assert (uncapped.reason, uncapped.enforcement) == ("no_limit", Enforcement.SOFT)
