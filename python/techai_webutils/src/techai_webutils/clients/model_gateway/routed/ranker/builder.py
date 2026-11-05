"""Config-selected ``CandidateRanker`` factory (``static_order`` or ``cost_aware``)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.ranker.static_order import (
    StaticOrderRanker,
)
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.routing import CandidateRanker
    from techai_webutils.core.interfaces.token_ledger import TokenLedger
    from techai_webutils.foundation.tokens.cost import PriceTable


class CandidateRankerKind(StrEnum):
    """The candidate ranker backends."""

    STATIC_ORDER = "static_order"
    """The route's models first, then catalog order (the default)."""
    COST_AWARE = "cost_aware"
    """By cost, latency, quality or a blend, within the tenant's hard caps."""


def candidate_ranker_from_config(
    raw: Mapping[str, Any],
    *,
    prices: PriceTable | None = None,
    ledger: TokenLedger | None = None,
    default_slo: str = "quality",
) -> CandidateRanker:
    """Build the ranker ``raw["kind"]`` selects (``static_order`` by default).

    Args:
        raw: The ranker config (``kind``; ``near_budget_ratio`` and ``weights`` for
            ``cost_aware``).
        prices: The price table (required by ``cost_aware``).
        ledger: The token ledger the budget bias reads (``cost_aware``; optional).
        default_slo: The objective when neither the request nor the policy names one.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``cost_aware`` without a price table.

    """
    kind = str(raw.get("kind", CandidateRankerKind.STATIC_ORDER))
    if kind == CandidateRankerKind.STATIC_ORDER:
        return StaticOrderRanker()
    if kind == CandidateRankerKind.COST_AWARE:
        if prices is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "cost_aware ranker requires a price table"
            )
        from techai_webutils.clients.model_gateway.routed.ranker.cost_aware import (  # noqa: PLC0415
            DEFAULT_WEIGHTS,
            CostAwareRanker,
        )

        return CostAwareRanker(
            prices,
            ledger=ledger,
            default_slo=default_slo,
            near_budget_ratio=float(raw.get("near_budget_ratio", 0.8)),
            weights=raw.get("weights", DEFAULT_WEIGHTS),
        )
    msg = f"unknown candidate ranker kind: {kind!r}"
    raise AppValueError(msg)
