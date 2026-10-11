"""Property and unit tests for the token-ledger ``Decimal`` cost arithmetic."""

from __future__ import annotations

from datetime import UTC, datetime
from decimal import Decimal

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.token_ledger.cost import ModelPrice, PriceTable, cost_of
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.token_ledger import UsageRecord, UsageScope

_TABLE = PriceTable(
    prices={
        "nova-lite": ModelPrice(
            Decimal("0.00006"), Decimal("0.00024"), Decimal("0.00002")
        )
    },
    version="2026-10",
)
_MICRO = Decimal("0.000001")


def _record(
    model: str, input_tokens: int, output_tokens: int, embed_tokens: int
) -> UsageRecord:
    """Return a usage record for ``model`` with the given token counts."""
    return UsageRecord(
        scope=UsageScope(org_id="org-1"),
        model=model,
        operation="generate",
        input_tokens=input_tokens,
        output_tokens=output_tokens,
        embed_tokens=embed_tokens,
        ts=datetime(2026, 10, 9, tzinfo=UTC),
    )


_tokens = st.integers(min_value=0, max_value=10_000_000)


@given(_tokens, _tokens, _tokens, st.integers(min_value=0, max_value=1_000_000))
def test_cost_is_non_negative_monotonic_and_exact(
    input_tokens: int, output_tokens: int, embed_tokens: int, extra: int
) -> None:
    """Test that cost is non-negative, monotonic in every token count, and exact to the micro-dollar.

    **Why this test is important:**
      - Chargeback reconciles to the cent across millions of records; float drift or a rounding
        mode that is not banker's would make totals disagree between services.

    **What it tests:**
      - ``cost_of`` is a ``Decimal`` ≥ 0 quantized to 6 places
      - it equals ``(in*p_in + out*p_out + embed*p_embed) / 1000`` rounded half-even
      - adding ``extra`` tokens to any one count never lowers the cost
    """
    cost = cost_of(
        _record("nova-lite", input_tokens, output_tokens, embed_tokens), _TABLE
    )
    price = _TABLE.prices["nova-lite"]
    exact = (
        input_tokens * price.input_per_1k
        + output_tokens * price.output_per_1k
        + embed_tokens * price.embed_per_1k
    ) / 1000

    assert isinstance(cost, Decimal)
    assert cost >= 0
    assert cost == exact.quantize(_MICRO, rounding="ROUND_HALF_EVEN")
    assert cost.as_tuple().exponent == -6
    assert (
        cost_of(
            _record("nova-lite", input_tokens + extra, output_tokens, embed_tokens),
            _TABLE,
        )
        >= cost
    )
    assert (
        cost_of(
            _record("nova-lite", input_tokens, output_tokens + extra, embed_tokens),
            _TABLE,
        )
        >= cost
    )
    assert (
        cost_of(
            _record("nova-lite", input_tokens, output_tokens, embed_tokens + extra),
            _TABLE,
        )
        >= cost
    )


def test_unknown_model_raises() -> None:
    """Test that a model with no price fails loudly instead of costing zero.

    **Why this test is important:**
      - A missing price that silently costs 0 makes a new model free in every budget and report.

    **What it tests:**
      - ``cost_of`` for an unpriced model raises ``AppError(INVALID_INPUT)`` naming the model
    """
    with pytest.raises(AppError) as caught:
        cost_of(_record("gpt-unknown", 1, 1, 0), _TABLE)

    assert caught.value.code is ErrorCode.INVALID_INPUT
    assert caught.value.details == {"model": "gpt-unknown", "pricing_version": "2026-10"}
