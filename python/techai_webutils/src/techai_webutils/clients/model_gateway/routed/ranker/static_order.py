"""The ``static_order`` ranker: the route's default model, its preferred models, then catalog order."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.types.routing import RankResult

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.types.routing import CatalogEndpointRef, PlanInput, Route
    from techai_webutils.foundation.tokens.cost import PriceTable


class StaticOrderRanker:
    """Orders candidates by the route's model preference; drops nothing."""

    @staticmethod
    async def rank(
        ctx: RouteContext,  # noqa: ARG004 - the static order ignores the request
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,  # noqa: ARG004 - the static order ignores the request
    ) -> RankResult:
        """Return the candidates with the route's models first, the rest in catalog order."""
        preference = list(dict.fromkeys((route.default_model, *route.preferred)))
        rank_of = {model_id: index for index, model_id in enumerate(preference)}
        last = len(preference)
        ordered = sorted(candidates, key=lambda ref: rank_of.get(ref.model_id, last))
        return RankResult(ordered=tuple(ordered))

    @classmethod
    async def rank_priced(
        cls,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
        prices: PriceTable,  # noqa: ARG003 - the static order ignores prices
    ) -> RankResult:
        """Return ``rank``'s order; the static order never reads the call's prices."""
        return await cls.rank(ctx, route, candidates, route_input)
