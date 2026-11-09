"""Unit tests for ``TokenAccountingProxy``: budget before the call, usage after it."""

from __future__ import annotations

import asyncio
import dataclasses
from datetime import UTC, datetime
from decimal import Decimal
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.decorators.token_accounting import (
    TokenAccountingOptions,
    TokenAccountingProxy,
)
from techai_webutils.core.errors import ErrorCode, QuotaExceededError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.llm import (
    LLMMessage,
    LLMProvider,
    LLMResponse,
    StreamUsage,
)
from techai_webutils.core.interfaces.semantic_cache import set_served_from_cache
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    TokenLedger,
    UsageRecord,
    UsageScope,
)
from techai_webutils.foundation.tokens.cost import ModelPrice, PriceTable
from techai_webutils.foundation.tokens.policy import BudgetPolicy, Enforcement

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

_NOW = datetime(2026, 10, 31, 12, 0, tzinfo=UTC)
_SCOPE = UsageScope(org_id="org-1", workspace_id="ws-1", user_id="u-1")
_PRICES = PriceTable(
    prices={"sonnet": ModelPrice(Decimal("0.003"), Decimal("0.015"), Decimal("0.0001"))},
    version="v1",
)
_MESSAGES = [LLMMessage(role="user", content="q")]
_OK = BudgetDecision(
    allowed=True, reason="within_budget", remaining_tokens=None, remaining_cost_usd=None
)
_SPENT = BudgetDecision(
    allowed=False,
    reason="budget_exhausted",
    remaining_tokens=0,
    remaining_cost_usd=Decimal(0),
)


def _ledger(decision: BudgetDecision | Exception = _OK) -> MagicMock:
    """Return a ledger whose ``check_budget`` answers ``decision`` (or raises it).

    Returns:
        A ledger whose ``check_budget`` answers ``decision`` (or raises it).

    """
    ledger = MagicMock(spec=TokenLedger)
    if isinstance(decision, Exception):
        ledger.check_budget = AsyncMock(side_effect=decision)
    else:
        ledger.check_budget = AsyncMock(return_value=decision)
    ledger.record = AsyncMock()
    return ledger


def _llm() -> MagicMock:
    """Return a provider answering 10 in / 20 out on ``sonnet``.

    Returns:
        A provider answering 10 in / 20 out on ``sonnet``.

    """
    llm = MagicMock(spec=LLMProvider)
    llm.complete = AsyncMock(
        return_value=LLMResponse(
            content="a",
            model="sonnet",
            input_tokens=10,
            output_tokens=20,
            finish_reason="stop",
        )
    )
    return llm


def _proxy(
    inner: MagicMock,
    ledger: MagicMock,
    *,
    enforcement: Enforcement = Enforcement.HARD,
    scope: UsageScope | None = _SCOPE,
) -> LLMProvider:
    """Wrap ``inner`` for the ``generate`` step with a fixed clock.

    (typed as the wrapped port).

    Returns:
        The provider proxy.

    """
    proxy = TokenAccountingProxy(
        inner,
        options=TokenAccountingOptions(
            ledger=ledger,
            scope=lambda: scope,
            prices=_PRICES,
            policy=BudgetPolicy(enforcement=enforcement),
            step="generate",
            clock=lambda: _NOW,
        ),
    )
    return cast("LLMProvider", proxy)


def _recorded(ledger: MagicMock) -> UsageRecord:
    """Return the one usage ``ledger`` recorded.

    Returns:
        The one usage ``ledger`` recorded.

    """
    ledger.record.assert_awaited_once()
    return ledger.record.await_args.args[0]


@pytest.mark.asyncio
async def test_rejects_before_calling_inner_when_over_budget_and_enforced() -> None:
    """Test that an exhausted, enforced budget rejects before any model call.

    **Why this test is important:**
      - A rejection after the call would still spend the tokens the budget exists to cap.

    **What it tests:**
      - ``QuotaExceededError`` (``RESOURCE_EXHAUSTED``) naming the org and the reason
      - the inner provider is never called and nothing is recorded
    """
    inner, ledger = _llm(), _ledger(_SPENT)

    with pytest.raises(QuotaExceededError) as caught:
        await _proxy(inner, ledger).complete(_MESSAGES)

    assert caught.value.code is ErrorCode.RESOURCE_EXHAUSTED
    assert caught.value.details == {"org_id": "org-1", "reason": "budget_exhausted"}
    inner.complete.assert_not_awaited()
    ledger.record.assert_not_awaited()


@pytest.mark.asyncio
async def test_records_usage_after_success_from_llm_response() -> None:
    """Test that a served call records its usage and cost against the scope.

    **Why this test is important:**
      - The ledger is the source of budgets and chargeback; a wrong field mis-bills a
        tenant.

    **What it tests:**
      - the response is returned unchanged
      - exactly one ``UsageRecord`` with the scope, model, step, tokens, cost and pricing
        version
    """
    inner, ledger = _llm(), _ledger()

    response = await _proxy(inner, ledger).complete(_MESSAGES)

    assert response.content == "a"
    assert _recorded(ledger) == UsageRecord(
        scope=_SCOPE,
        model="sonnet",
        operation="generate",
        input_tokens=10,
        output_tokens=20,
        ts=_NOW,
        cost_usd=Decimal("0.000330"),
        pricing_version="v1",
        trace_id="",
    )


async def _items(*items: str | StreamUsage) -> AsyncIterator[str | StreamUsage]:
    """Yield ``items``.

    Yields:
        ``items``.

    """
    for item in items:
        await asyncio.sleep(0)
        yield item


@pytest.mark.asyncio
async def test_records_stream_usage_from_terminal_stream_usage() -> None:
    """Test that a stream records the usage its terminal ``StreamUsage`` reports.

    **Why this test is important:**
      - The chat path streams; a stream that went unrecorded would be free.

    **What it tests:**
      - ``stream`` yields text only; ``stream_with_usage`` keeps the ``StreamUsage``
      - each records one usage with the streamed token counts once exhausted
    """
    usage = StreamUsage(
        model="sonnet", input_tokens=4, output_tokens=6, finish_reason="stop"
    )
    inner, ledger = _llm(), _ledger()
    inner.stream_with_usage = AsyncMock(
        side_effect=lambda *_a, **_k: _items("he", "llo", usage)
    )
    proxy = _proxy(inner, ledger)

    text = [item async for item in await proxy.stream(_MESSAGES)]
    record = _recorded(ledger)
    ledger.record.reset_mock()
    full = [item async for item in await proxy.stream_with_usage(_MESSAGES)]

    assert text == ["he", "llo"]
    assert full == ["he", "llo", usage]
    assert (record.input_tokens, record.output_tokens, record.model) == (4, 6, "sonnet")
    assert _recorded(ledger).cost_usd == Decimal("0.000102")


@pytest.mark.asyncio
async def test_shadow_mode_serves_and_records_but_never_rejects() -> None:
    """Test that shadow enforcement serves an over-budget scope and counts it.

    **Why this test is important:**
      - Shadow is how a budget is dual-run before it is enforced; it must never block.

    **What it tests:**
      - an exhausted budget under ``SHADOW`` still calls the inner and records usage
      - ``ai_budget_would_exceed_total`` is incremented
    """
    inner, ledger = _llm(), _ledger(_SPENT)
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock()
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    proxy = cast(
        "LLMProvider",
        TokenAccountingProxy(
            inner,
            options=TokenAccountingOptions(
                ledger=ledger,
                scope=lambda: _SCOPE,
                prices=_PRICES,
                policy=BudgetPolicy(enforcement=Enforcement.SHADOW),
                step="generate",
                metrics=metrics,
                clock=lambda: _NOW,
            ),
        ),
    )

    await proxy.complete(_MESSAGES)

    inner.complete.assert_awaited_once()
    _recorded(ledger)
    counters["ai_budget_would_exceed_total"].inc.assert_called_once_with(1.0, org="org-1")
    counters["ai_tokens_total"].inc.assert_any_call(
        10, org="org-1", workspace="ws-1", model="sonnet", type="input"
    )


@pytest.mark.asyncio
async def test_fail_open_ledger_error_still_serves() -> None:
    """Test that a ledger outage never blocks a model call.

    **Why this test is important:**
      - The ledger guards spend; its outage must not become a product outage.

    **What it tests:**
      - ``check_budget`` raising under ``soft`` → the call is served and its usage still
        recorded
      - ``record`` raising → the response is still returned
    """
    inner, ledger = _llm(), _ledger(RuntimeError("redis down"))
    ledger.record = AsyncMock(side_effect=RuntimeError("redis down"))

    response = await _proxy(inner, ledger, enforcement=Enforcement.SOFT).complete(
        _MESSAGES
    )

    assert response.model == "sonnet"
    inner.complete.assert_awaited_once()


@pytest.mark.asyncio
async def test_no_scope_is_passthrough() -> None:
    """Test that a call outside any request is neither gated nor recorded.

    **Why this test is important:**
      - Boot-time and background calls have no tenant to charge; they must still work.

    **What it tests:**
      - with no scope the inner answers and the ledger is never consulted
      - non-model attributes pass through untouched
    """
    inner, ledger = _llm(), _ledger(_SPENT)
    inner.model_name = MagicMock(return_value="sonnet")
    proxy = _proxy(inner, ledger, scope=None)

    await proxy.complete(_MESSAGES)

    assert proxy.model_name() == "sonnet"
    ledger.check_budget.assert_not_awaited()
    ledger.record.assert_not_awaited()


@pytest.mark.asyncio
async def test_cache_hit_records_one_zero_token_usage() -> None:
    """Test that an answer served from the semantic cache records one zero-cost usage.

    **Why this test is important:**
      - A cache hit must be visible in usage (hit rate per tenant) without billing the
        tokens of the answer it replayed.

    **What it tests:**
      - when the inner marks the call ``served_from_cache``, one usage is recorded with
        ``operation="generate.cache_hit"``, zero tokens and zero cost
    """
    inner, ledger = _llm(), _ledger()
    response = inner.complete.return_value

    async def cached(*_args: object, **_kwargs: object) -> LLMResponse:
        await asyncio.sleep(0)
        set_served_from_cache(value=True)
        return response

    inner.complete = AsyncMock(side_effect=cached)

    await _proxy(inner, ledger).complete(_MESSAGES)

    record = _recorded(ledger)
    assert (
        record.operation,
        record.input_tokens,
        record.output_tokens,
        record.cost_usd,
    ) == (
        "generate.cache_hit",
        0,
        0,
        Decimal(0),
    )


@pytest.mark.asyncio
async def test_embedding_calls_record_embed_tokens_and_unpriced_models_record_zero() -> (
    None
):
    """Test that embeddings are charged by embedding tokens.

    And an unpriced model costs zero.

    **Why this test is important:**
      - Embeddings are part of a tenant's spend; an unpriced model must be recorded (and
        counted for the operator), never fail the call.

    **What it tests:**
      - ``embed_batch`` records one usage with the summed ``embed_tokens``
      - an unpriced model records ``cost_usd=0`` with ``pricing_version="missing"``
    """
    inner, ledger = MagicMock(spec=EmbeddingProvider), _ledger()
    inner.embed_batch = AsyncMock(
        return_value=[
            EmbeddingResult(embedding=[0.0], model="titan", token_count=3),
            EmbeddingResult(embedding=[0.0], model="titan", token_count=4),
        ]
    )

    await cast("EmbeddingProvider", _proxy(inner, ledger)).embed_batch(["a", "b"])

    record = _recorded(ledger)
    assert (
        record.model,
        record.embed_tokens,
        record.cost_usd,
        record.pricing_version,
    ) == (
        "titan",
        7,
        Decimal(0),
        "missing",
    )


@pytest.mark.asyncio
async def test_hard_ledger_error_rejects_before_inner() -> None:
    """Test that hard enforcement fails closed when the ledger can't answer.

    **Why this test is important:**
      - Under a hard cap a ledger outage must not become free, unlimited spend.

    **What it tests:**
      - a raising ``check_budget`` under ``HARD`` raises ``QuotaExceededError`` with
        reason ``ledger_error_fail_closed``; the inner is never called
      - the same failure under ``SOFT`` serves
    """
    inner = _llm()

    with pytest.raises(QuotaExceededError) as caught:
        await _proxy(inner, _ledger(RuntimeError("down"))).complete(_MESSAGES)
    inner.complete.assert_not_awaited()
    await _proxy(
        inner, _ledger(RuntimeError("down")), enforcement=Enforcement.SOFT
    ).complete(_MESSAGES)

    assert caught.value.details["reason"] == "ledger_error_fail_closed"
    inner.complete.assert_awaited_once()


@pytest.mark.asyncio
async def test_per_budget_enforces_the_budget_level_under_a_shadow_policy() -> None:
    """Test that a ``soft`` budget refuses only when the policy opts into ``per_budget``.

    **Why this test is important:**
      - An admin's ``soft`` budget must hold while the deployment stays ``shadow``, but a
        deployment that has not opted in must keep serving exactly as before.

    **What it tests:**
      - a ``SHADOW`` policy serves an exhausted budget stored as ``soft``
      - the same policy with ``per_budget=True`` raises ``QuotaExceededError`` before the
        inner call and records nothing
    """
    soft_spent = dataclasses.replace(_SPENT, enforcement=Enforcement.SOFT)

    def wrap(inner: MagicMock, ledger: MagicMock, *, per_budget: bool) -> LLMProvider:
        policy = BudgetPolicy(enforcement=Enforcement.SHADOW, per_budget=per_budget)
        proxy = TokenAccountingProxy(
            inner,
            options=TokenAccountingOptions(
                ledger=ledger,
                scope=lambda: _SCOPE,
                prices=_PRICES,
                policy=policy,
                step="generate",
                clock=lambda: _NOW,
            ),
        )
        return cast("LLMProvider", proxy)

    served_inner, served_ledger = _llm(), _ledger(soft_spent)
    await wrap(served_inner, served_ledger, per_budget=False).complete(_MESSAGES)
    refused_inner, refused_ledger = _llm(), _ledger(soft_spent)
    with pytest.raises(QuotaExceededError):
        await wrap(refused_inner, refused_ledger, per_budget=True).complete(_MESSAGES)

    served_inner.complete.assert_awaited_once()
    refused_inner.complete.assert_not_awaited()
    refused_ledger.record.assert_not_awaited()
