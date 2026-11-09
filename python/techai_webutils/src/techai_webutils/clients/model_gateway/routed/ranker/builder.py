"""Config-selected ``CandidateRanker`` factory (``static_order`` or ``cost_aware``).

Every ranker it builds is a ``PricedCandidateRanker``: besides the core ``rank`` (under
the table bound at construction, if any) it ranks under a table passed per call, so a
caller whose prices arrive with each request (a remote planner) builds one ranker at boot.
"""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any, Protocol

from techai_webutils.clients.model_gateway.routed.ranker.static_order import (
    StaticOrderRanker,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.routing import CandidateRanker

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.interfaces.token_ledger import TokenLedger
    from techai_webutils.core.types.routing import (
        CatalogEndpointRef,
        PlanInput,
        RankResult,
        Route,
    )
    from techai_webutils.foundation.tokens.cost import PriceTable


class PricedCandidateRanker(CandidateRanker, Protocol):
    """A ``CandidateRanker`` that can also rank under a price table passed.

    With the call.
    """

    async def rank_priced(
        self,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
        prices: PriceTable,
    ) -> RankResult:
        """Return the candidates best first.

        Costed from ``prices`` instead of a bound table.

        ``prices`` holds this request's prices only; a ranker that does not rank by cost
        ignores it.
        """
        ...


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
) -> PricedCandidateRanker:
    """Build the ranker ``raw["kind"]`` selects (``static_order`` by default).

    Args:
        raw: The ranker config (``kind``; ``near_budget_ratio`` and ``weights`` for
            ``cost_aware``).
        prices: The table ``rank`` costs from (``cost_aware``). Without it a
            ``cost_aware`` ranker ranks only through ``rank_priced``, under each call's
            own table.
        ledger: The token ledger the budget bias reads (``cost_aware``; optional).
        default_slo: The objective when neither the request nor the policy names one.

    Returns:
        The ranker ``raw["kind"]`` selects (``static_order`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    kind = str(raw.get("kind", CandidateRankerKind.STATIC_ORDER))
    if kind == CandidateRankerKind.STATIC_ORDER:
        return StaticOrderRanker()
    if kind == CandidateRankerKind.COST_AWARE:
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
