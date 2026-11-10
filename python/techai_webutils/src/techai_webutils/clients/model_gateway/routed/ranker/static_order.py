"""The ``static_order`` ranker.

The route's default model, its preferred models, then catalog order.
"""

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
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
    ) -> RankResult:
        """Return the candidates with the route's models first, the rest in catalog order.

        Args:
            ctx: The request's routing context.
            route: The route being planned.
            candidates: The endpoints the policy filter allowed.
            route_input: The request's size and requirements.

        Returns:
            The candidates with the route's models first.

        """
        del ctx, route_input
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
        prices: PriceTable,
    ) -> RankResult:
        """Return ``rank``'s order; the static order never reads the call's prices.

        Args:
            ctx: Ignored.
            route: The call's route.
            candidates: The endpoints that passed the filter.
            route_input: Ignored.
            prices: Ignored.

        Returns:
            ``rank``'s order; the static order never reads the call's prices.

        """
        del prices
        return await cls.rank(ctx, route, candidates, route_input)
