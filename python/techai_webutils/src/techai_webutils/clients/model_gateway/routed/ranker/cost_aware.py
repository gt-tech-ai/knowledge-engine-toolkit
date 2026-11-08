"""The ``cost_aware`` ranker: order candidates by an objective, within hard caps.

The objective is the request's ``slo``, else the tenant policy's ``objective``, else the
gateway's default: ``cost`` (estimated request cost from the price table: the one bound at
construction for ``rank``, the call's own for ``rank_priced``; unpriced sorts last),
``latency`` (time-to-first-token prior), ``quality`` (quality prior) or ``balanced`` (a weighted
sum of the three, each normalised to ``[0, 1]`` across the candidates). Ties keep catalog order.

- **Hard caps** drop, never demote: a candidate over ``max_cost_per_request`` or
  ``max_latency_ms`` is rejected as ``over_cap``; when the caps drop everything the request is
  ``FORBIDDEN``, never an over-cap call.
- **Quality floor:** a candidate below the route's ``quality_floor`` is rejected as
  ``quality_floor`` unless the policy sets ``degrade_on_budget``.
- **Budget bias:** when the tenant has used ``near_budget_ratio`` of its cost budget (the injected
  ledger's month usage against usage + remaining), the objective becomes ``cost``. A ledger error
  means no bias (fail-open).
"""

from __future__ import annotations

from decimal import Decimal
from types import MappingProxyType
from typing import TYPE_CHECKING

from techai_webutils.clients.token_ledger.cost import estimate_cost
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.token_ledger import Period, UsageScope
from techai_webutils.core.types.routing import RankResult

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.clients.token_ledger.cost import PriceTable
    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.interfaces.token_ledger import TokenLedger
    from techai_webutils.core.types.routing import CatalogEndpointRef, PlanInput, Route

_RANK_TOKEN_FLOOR = 1_000
"""Token count the cost score assumes at least, per direction (caps use the real estimate)."""

DEFAULT_WEIGHTS: Mapping[str, float] = MappingProxyType({
    "quality": 0.5,
    "cost": 0.3,
    "latency": 0.2,
})
"""The ``balanced`` objective's weights."""


def _normalised(values: Sequence[float], *, higher_is_better: bool) -> list[float]:
    """Scale ``values`` to ``[0, 1]`` with 1 the best (all equal → all 1)."""
    low, high = min(values), max(values)
    if high == low:
        return [1.0] * len(values)
    return [
        ((v - low) if higher_is_better else (high - v)) / (high - low) for v in values
    ]


class CostAwareRanker:
    """Ranks candidates by cost, latency, quality or a blend, within the tenant's caps."""

    def __init__(
        self,
        prices: PriceTable | None = None,
        *,
        ledger: TokenLedger | None = None,
        default_slo: str = "quality",
        near_budget_ratio: float = 0.8,
        weights: Mapping[str, float] = DEFAULT_WEIGHTS,
    ) -> None:
        """Bind the optional price table and ledger and the ranking defaults.

        Without ``prices`` the ranker ranks only through ``rank_priced``.
        """
        self._prices = prices
        self._ledger = ledger
        self._default_slo = default_slo
        self._near_budget_ratio = near_budget_ratio
        self._weights = weights

    async def _near_budget(self, ctx: RouteContext) -> bool:
        """Return whether the tenant has used ``near_budget_ratio`` of its cost budget."""
        if self._ledger is None or not ctx.tenant_id:
            return False
        scope = UsageScope(org_id=ctx.tenant_id, workspace_id=ctx.workspace_id or None)
        try:
            decision = await self._ledger.check_budget(scope)
            used = (await self._ledger.usage(scope, Period.MONTHLY)).cost_usd
        except Exception:
            return False
        if decision.remaining_cost_usd is None:
            return False
        limit = used + decision.remaining_cost_usd
        return limit > 0 and used / limit >= Decimal(str(self._near_budget_ratio))

    @staticmethod
    def _estimate(
        ref: CatalogEndpointRef,
        route_input: PlanInput,
        prices: PriceTable,
        *,
        floor: int = 0,
    ) -> Decimal | None:
        """Return ``ref``'s estimated cost for this request (``None`` when unpriced).

        ``floor`` raises each token count to at least that many, so a request whose size is
        unknown (no ``max_tokens``, a tiny prompt) still ranks by relative price instead of
        tying at zero.
        """
        req = route_input.requirements
        return estimate_cost(
            ref.model_id,
            prices,
            input_tokens=max(req.prompt_tokens, floor),
            output_tokens=max(req.max_tokens, floor),
        )

    def _rejection(
        self,
        ref: CatalogEndpointRef,
        route: Route,
        route_input: PlanInput,
        prices: PriceTable,
    ) -> str | None:
        """Return why ``ref`` is dropped (``over_cap``, ``quality_floor``), or ``None``."""
        policy = route_input.policy
        estimate = self._estimate(ref, route_input, prices)
        if (
            policy.max_cost_per_request is not None
            and estimate is not None
            and estimate > policy.max_cost_per_request
        ) or (policy.max_latency_ms is not None and ref.ttft_ms > policy.max_latency_ms):
            return "over_cap"
        if ref.quality < route.quality_floor and not policy.degrade_on_budget:
            return "quality_floor"
        return None

    def _scores(
        self,
        objective: str,
        kept: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
        prices: PriceTable,
    ) -> list[float]:
        """Return each candidate's score under ``objective`` (higher is better)."""
        estimates = [
            self._estimate(ref, route_input, prices, floor=_RANK_TOKEN_FLOOR)
            for ref in kept
        ]
        worst = max((e for e in estimates if e is not None), default=Decimal(0))
        costs = [float(e if e is not None else worst + 1) for e in estimates]
        cost = _normalised(costs, higher_is_better=False)
        latency = _normalised([float(r.ttft_ms) for r in kept], higher_is_better=False)
        quality = _normalised([r.quality for r in kept], higher_is_better=True)
        if objective == "cost":
            return cost
        if objective == "latency":
            return latency
        if objective == "balanced":
            w = self._weights
            return [
                w.get("quality", 0) * q + w.get("cost", 0) * c + w.get("latency", 0) * t
                for q, c, t in zip(quality, cost, latency, strict=True)
            ]
        return quality

    async def rank(
        self,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
    ) -> RankResult:
        """Return the candidates within the caps, best first, under the bound price table.

        Raises:
            AppError: ``INVALID_INPUT`` when the ranker was built without a price table;
                ``FORBIDDEN`` when the caps and floor drop every candidate.

        """
        if self._prices is None:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                "cost_aware ranker has no price table; rank with rank_priced",
            )
        return await self.rank_priced(ctx, route, candidates, route_input, self._prices)

    async def rank_priced(
        self,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
        prices: PriceTable,
    ) -> RankResult:
        """Return the candidates within the caps, best first, costed from ``prices``.

        Raises:
            AppError: ``FORBIDDEN`` when the caps and floor drop every candidate.

        """
        rejections: dict[str, str] = {}
        kept: list[CatalogEndpointRef] = []
        for ref in candidates:
            reason = self._rejection(ref, route, route_input, prices)
            if reason is None:
                kept.append(ref)
            else:
                rejections[ref.key] = reason
        if not kept:
            raise AppError(ErrorCode.FORBIDDEN, "routing caps allow no endpoint")
        objective = ctx.slo or route_input.policy.objective or self._default_slo
        if await self._near_budget(ctx):
            objective = "cost"
        scores = self._scores(objective, kept, route_input, prices)
        order = sorted(range(len(kept)), key=lambda i: -scores[i])
        return RankResult(
            ordered=tuple(kept[i] for i in order), rejections=MappingProxyType(rejections)
        )
