"""The routed gateway's stage contracts: classify, rank, plan, and the policy store.

Each stage is a small protocol with a ``Kind`` factory in the client tier, so a smarter stage
(a semantic classifier, a cost-aware ranker, a remote planner) is a config change.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.types.routing import (
        CatalogEndpointRef,
        Plan,
        PlanInput,
        RankResult,
        Route,
        RoutingPolicy,
    )


class RouteClassifier(Protocol):
    """Chooses the route (task class) for a request."""

    async def classify(self, ctx: RouteContext, query: str) -> tuple[str, float]:
        """Return the route name and the confidence in it."""
        ...


class CandidateRanker(Protocol):
    """Orders the filtered candidates for a route."""

    async def rank(
        self, ctx: RouteContext, route: Route, candidates: Sequence[CatalogEndpointRef]
    ) -> RankResult:
        """Return the candidates best first, with any the ranker dropped."""
        ...


class RoutePlanner(Protocol):
    """Turns a request and its filtered candidates into an ordered plan."""

    async def plan(self, ctx: RouteContext, route_input: PlanInput) -> Plan:
        """Return the endpoints to try, in order."""
        ...


class RoutingPolicyStore(Protocol):
    """Supplies the routing policy for a tenant and workspace."""

    def policy(self, tenant_id: str, workspace_id: str) -> RoutingPolicy:
        """Return the workspace's policy, else the tenant's, else the default policy."""
        ...
