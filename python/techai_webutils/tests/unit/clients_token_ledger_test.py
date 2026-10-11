"""Unit tests for the ``TokenLedger`` contract, its config-selected factory and the stub backend."""

from __future__ import annotations

import dataclasses
from datetime import UTC, datetime
from decimal import Decimal
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.token_ledger import (
    TokenLedgerConfig,
    TokenLedgerKind,
    token_ledger_from_config,
)
from techai_webutils.clients.token_ledger.stub import StubTokenLedger
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    Period,
    TokenLedger,
    UsageRecord,
    UsageScope,
    UsageSummary,
)


def _record() -> UsageRecord:
    """Return a usage record for one generation."""
    return UsageRecord(
        scope=UsageScope(org_id="org-1", team_id="t-1"),
        model="nova-lite",
        operation="generate",
        input_tokens=100,
        output_tokens=20,
        ts=datetime(2026, 10, 9, tzinfo=UTC),
    )


def test_usage_scope_requires_org_id() -> None:
    """Test that a usage scope cannot be built without an organization.

    **Why this test is important:**
      - Every counter and budget is keyed by org; an org-less record would be charged to nobody
        and silently escape budget enforcement.

    **What it tests:**
      - ``UsageScope(org_id="")`` raises ``AppError(INVALID_INPUT)``
      - the optional team/workspace/user default to ``None``
    """
    with pytest.raises(AppError) as caught:
        UsageScope(org_id="")

    assert caught.value.code is ErrorCode.INVALID_INPUT
    assert UsageScope(org_id="org-1") == UsageScope(
        org_id="org-1", team_id=None, workspace_id=None, user_id=None
    )


def test_usage_records_are_frozen() -> None:
    """Test that usage records and scopes are immutable values.

    **Why this test is important:**
      - A record is costed, counted and streamed by different layers; a mutation between them
        would charge one amount and stream another.

    **What it tests:**
      - assigning a field on a ``UsageRecord`` or ``UsageScope`` raises ``FrozenInstanceError``
      - ``embed_tokens`` defaults to 0 and ``cost_usd`` to ``Decimal("0")``
    """
    record = _record()

    with pytest.raises(dataclasses.FrozenInstanceError):
        record.input_tokens = 1  # type: ignore[misc]
    with pytest.raises(dataclasses.FrozenInstanceError):
        record.scope.org_id = "other"  # type: ignore[misc]
    assert record.embed_tokens == 0
    assert record.cost_usd == Decimal("0")


def test_factory_returns_stub_by_default() -> None:
    """Test that the default config builds the stub ledger with zero infrastructure.

    **Why this test is important:**
      - The graph must boot in dev and CI with no Redis and no ledger service.

    **What it tests:**
      - ``token_ledger_from_config(TokenLedgerConfig())`` is a ``StubTokenLedger`` and a ``TokenLedger``
      - the default window is monthly and the stream contract name is ``token_ledger:usage``
    """
    config = TokenLedgerConfig()

    ledger = token_ledger_from_config(config)

    assert isinstance(ledger, StubTokenLedger)
    assert isinstance(ledger, TokenLedger)
    assert config.kind == TokenLedgerKind.STUB
    assert config.window is Period.MONTHLY
    assert config.stream == "token_ledger:usage"


def test_factory_uses_injected_backend_for_extra_kind() -> None:
    """Test that a consumer-supplied kind is built by its injected backend factory.

    **Why this test is important:**
      - The durable ledger backend calls a product service the KE must not import; injecting it
        keeps the KE product-free while the kind stays a config value.

    **What it tests:**
      - ``kind="grpc"`` with ``backends={"grpc": factory}`` returns exactly what the factory
        returns, and the factory receives the config
    """
    built = MagicMock(spec=TokenLedger)
    factory = MagicMock(return_value=built)
    config = TokenLedgerConfig(kind="grpc")

    ledger = token_ledger_from_config(config, backends={"grpc": factory})

    assert ledger is built
    factory.assert_called_once_with(config)


def test_factory_unknown_kind_raises_coded_error() -> None:
    """Test that an unknown kind, or redis without a client, fails loudly with a coded error.

    **Why this test is important:**
      - A typo'd kind must fail at startup, not silently fall back to allow-all.

    **What it tests:**
      - ``kind="mongo"`` and ``kind="redis"`` with no client each raise ``AppError(INVALID_INPUT)``
    """
    for config in (
        TokenLedgerConfig(kind="mongo"),
        TokenLedgerConfig(kind=TokenLedgerKind.REDIS),
    ):
        with pytest.raises(AppError) as caught:
            token_ledger_from_config(config)
        assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_stub_allows_all_and_records_nothing() -> None:
    """Test that the stub allows every call, records nothing and reports empty usage.

    **Why this test is important:**
      - The stub is what dev runs; it must never block a call and never pretend to have data.

    **What it tests:**
      - ``check_budget`` → ``BudgetDecision(True, "shadow", None, None)``
      - ``record`` returns ``None`` and ``usage`` → an all-zero ``UsageSummary`` for the scope/period
      - the stub satisfies the ``TokenLedger`` interface and works as an async context manager
    """
    scope = UsageScope(org_id="org-1")

    async with StubTokenLedger() as ledger:
        decision = await ledger.check_budget(scope)
        recorded = await ledger.record(_record())
        summary = await ledger.usage(scope, Period.DAILY)

    assert decision == BudgetDecision(
        allowed=True, reason="shadow", remaining_tokens=None, remaining_cost_usd=None
    )
    assert recorded is None
    assert summary == UsageSummary(
        scope=scope,
        period=Period.DAILY,
        input_tokens=0,
        output_tokens=0,
        embed_tokens=0,
        cost_usd=Decimal("0"),
    )
