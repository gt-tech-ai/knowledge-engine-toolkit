"""The ``local`` route planner: classify, then rank, in process."""

from __future__ import annotations

from typing import TYPE_CHECKING, final

from techai_webutils.core.types.routing import Plan

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.interfaces.routing import CandidateRanker, RouteClassifier
    from techai_webutils.core.types.routing import PlanInput, Route


@final
class LocalPlanner:
    """Plans by running the injected classifier.

    And ranker over the filtered candidates.
    """

    def __init__(
        self,
        classifier: RouteClassifier,
        ranker: CandidateRanker,
        routes: Mapping[str, Route],
    ) -> None:
        """Bind the stages and the parsed routes."""
        self._classifier = classifier
        self._ranker = ranker
        self._routes = routes

    async def plan(self, ctx: RouteContext, route_input: PlanInput) -> Plan:
        """Classify the query into a route, then rank the candidates for it.

        Returns:
            The plan.

        """
        classified = await self._classifier.classify(ctx, route_input.query)
        ranked = await self._ranker.rank(
            ctx, self._routes[classified.route], route_input.candidates, route_input
        )
        return Plan(
            route=classified.route,
            confidence=classified.confidence,
            ordered=ranked.ordered,
            decider="local",
            reason="classifier_degraded" if classified.degraded else "planned",
            rejections=ranked.rejections,
        )
