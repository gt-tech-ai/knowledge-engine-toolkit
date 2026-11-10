"""Config-selected ``CandidateRanker`` factory (``static_order`` or ``cost_aware``).

Every ranker it builds is a ``PricedCandidateRanker``: besides the core ``rank`` (under
the table bound at construction, if any) it ranks under a table passed per call, so a
caller whose prices arrive with each request (a remote planner) builds one ranker at boot.
"""

from __future__ import annotations

import importlib
from enum import StrEnum
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.clients.model_gateway.routed.ranker.static_order import (
    StaticOrderRanker,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.routing import CandidateRanker

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.clients.model_gateway.routed.ranker.cost_aware import (
        CostAwareRanker as _CostAwareRanker,
    )
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
    """A ``CandidateRanker`` that can also rank under a price table.

    The table is passed with the call.
    """

    async def rank_priced(
        self,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
        prices: PriceTable,
    ) -> RankResult:
        """Return the candidates best first, costed from ``prices``.

        ``prices`` replaces a bound table.

        ``prices`` holds this request's prices only; a ranker that does not rank by cost
        ignores it.

        Args:
            ctx: The call's route context.
            route: The call's route.
            candidates: The endpoints that passed the filter.
            route_input: The plan's input: the query, its requirements and the step.
            prices: Prices each model's tokens.

        Returns:
            The ordered candidates and why any were dropped.

        """
        ...


class CandidateRankerKind(StrEnum):
    """The candidate ranker backends."""

    STATIC_ORDER = "static_order"
    """The route's models first, then catalog order (the default)."""
    COST_AWARE = "cost_aware"
    """By cost, latency, quality or a blend, within the tenant's hard caps."""


def candidate_ranker_from_config(
    raw: Mapping[str, object],
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
        cost_aware = importlib.import_module(
            "techai_webutils.clients.model_gateway.routed.ranker.cost_aware"
        )
        default_weights = cast("Mapping[str, float]", cost_aware.DEFAULT_WEIGHTS)
        cost_aware_ranker_cls = cast("type[_CostAwareRanker]", cost_aware.CostAwareRanker)

        return cost_aware_ranker_cls(
            prices,
            ledger=ledger,
            default_slo=default_slo,
            near_budget_ratio=float(cast("float", raw.get("near_budget_ratio", 0.8))),
            weights=cast("Mapping[str, float]", raw.get("weights", default_weights)),
        )
    msg = f"unknown candidate ranker kind: {kind!r}"
    raise AppValueError(msg)
