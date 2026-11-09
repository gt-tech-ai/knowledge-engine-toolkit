"""The routed gateway's stage contracts: classify, rank, plan, and the policy store.

Each stage is a small protocol with a ``Kind`` factory in the client tier, so a smarter
stage (a semantic classifier, a cost-aware ranker, a remote planner) is a config change.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.model_gateway import RouteContext, RouteDecision
    from techai_webutils.core.types.routing import (
        CatalogEndpointRef,
        Classification,
        Plan,
        PlanInput,
        PlanRequest,
        RankResult,
        Route,
        RoutingPlan,
        RoutingPolicy,
    )


class RouteClassifier(Protocol):
    """Chooses the route (task class) for a request."""

    async def classify(self, ctx: RouteContext, query: str) -> Classification:
        """Return the route and the confidence in it.

        Never raise for a classifier outage.
        """
        ...


class CandidateRanker(Protocol):
    """Orders the filtered candidates for a route."""

    async def rank(
        self,
        ctx: RouteContext,
        route: Route,
        candidates: Sequence[CatalogEndpointRef],
        route_input: PlanInput,
    ) -> RankResult:
        """Return the candidates best first, with any the ranker dropped.

        ``route_input`` carries the request's requirements and the tenant policy
        (objective, hard caps, quality-floor rules).
        """
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


class RouteDecisionSink(Protocol):
    """Receives each finalized ``RouteDecision`` (live and shadow) for analysis."""

    def record(self, decision: RouteDecision, ctx: RouteContext) -> None:
        """Hand ``decision`` off without blocking; the gateway counts.

        (never raises) a failure.
        """
        ...


class RouteTransport(Protocol):
    """Carries a ``PlanRequest`` to a remote planner.

    (the product's gRPC client implements it).
    """

    async def route(
        self,
        request: PlanRequest,
        *,
        timeout_s: float,
        metadata: Sequence[tuple[str, str]],
    ) -> RoutingPlan:
        """Return the remote plan; raise a coded ``AppError``.

        (``UNAVAILABLE`` when transient).
        """
        ...
