"""Unit tests for the ``overlord`` route planner: a remote plan.

Failing open to ``local``.
"""

from __future__ import annotations

import asyncio
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.clients.model_gateway.routed.classifier import (
    route_classifier_from_config,
)
from techai_webutils.clients.model_gateway.routed.planner import route_planner_from_config
from techai_webutils.clients.model_gateway.routed.ranker import (
    candidate_ranker_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.interfaces.routing import RoutePlanner, RouteTransport
from techai_webutils.core.types.routing import PlanInput, PlanRequest, RoutingPlan

_CTX = RouteContext(tenant_id="org-1", workspace_id="ws-1", step="generate")


def _planner(
    catalog_raw: dict[str, Any],
    routes_raw: dict[str, Any],
    transport: Any,  # noqa: ANN401 - a RouteTransport mock (or None)
    *,
    raw: dict[str, Any] | None = None,
    metrics: MetricsProvider | None = None,
) -> tuple[RoutePlanner, ModelCatalog]:
    """Build an overlord planner over the fixture catalog, failing open to ``local``."""
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    routes = parse_routes(routes_raw, catalog)
    config = raw or {
        "kind": "overlord",
        "overlord": {"target": "overlord:8097", "timeout_ms": 50, "service_token": "tok"},
    }
    planner = route_planner_from_config(
        config,
        classifier=route_classifier_from_config(
            {}, route_names=frozenset(routes), default_route=""
        ),
        ranker=candidate_ranker_from_config({}),
        routes=routes,
        planner_deps={"transport": transport},
        metrics=metrics,
    )
    return planner, catalog


def _transport(
    plan: RoutingPlan | Exception | None = None, *, delay: float = 0.0
) -> MagicMock:
    """Return a transport answering ``plan`` (or raising it) after ``delay`` seconds."""

    async def route(request: PlanRequest, **_kwargs: object) -> RoutingPlan:
        await asyncio.sleep(delay)
        if isinstance(plan, Exception):
            raise plan
        assert request.candidates
        return plan or RoutingPlan()

    transport = MagicMock(spec=RouteTransport)
    transport.route = AsyncMock(side_effect=route)
    return transport


def _input(catalog: ModelCatalog) -> PlanInput:
    """Return a plan input offering the whole catalog."""
    return PlanInput(query="how long is the refund window", candidates=catalog.refs)


@pytest.mark.asyncio
async def test_overlord_planner_returns_plan_from_stub(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the overlord's plan becomes the ordered endpoint chain.

    **Why this test is important:**
      - The executor consumes one chain whoever planned; the overlord's model and
        fallbacks must map onto the offered endpoints in order.

    **What it tests:**
      - model ``haiku`` then fallback ``sonnet`` → haiku's endpoint, then sonnet's two
      - the decision is ``decider=overlord`` with the plan's task class and confidence
    """
    plan = RoutingPlan(
        task_class="lookup",
        model_id="anthropic/haiku",
        fallback_model_ids=("anthropic/sonnet",),
        confidence=0.8,
    )
    planner, catalog = _planner(catalog_raw, routes_raw, _transport(plan))

    result = await planner.plan(_CTX, _input(catalog))

    assert [ref.key for ref in result.ordered] == [
        "anthropic/haiku@use1",
        "anthropic/sonnet@use1",
        "anthropic/sonnet@usw2",
    ]
    assert (result.decider, result.route, result.confidence) == (
        "overlord",
        "lookup",
        0.8,
    )


@pytest.mark.asyncio
async def test_overlord_planner_times_out_to_local(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a slow overlord is abandoned at its timeout.

    And the local planner answers.

    **Why this test is important:**
      - The overlord is on the answer's critical path; it may cost at most its timeout.

    **What it tests:**
      - a transport slower than the 50 ms timeout → the local plan (Sonnet first),
        ``decider=local``, ``reason=planner_fail_open``, counted with ``cause=timeout``
    """
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    planner, catalog = _planner(
        catalog_raw, routes_raw, _transport(delay=1.0), metrics=metrics
    )

    result = await planner.plan(_CTX, _input(catalog))

    assert result.ordered[0].key == "anthropic/sonnet@use1"
    assert (result.decider, result.reason) == ("local", "planner_fail_open")
    counters["routed_planner_fail_open_total"].inc.assert_called_once_with(
        1.0, cause="timeout"
    )


@pytest.mark.asyncio
async def test_overlord_planner_unavailable_to_local(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that an unavailable overlord is retried once, then fails open.

    **Why this test is important:**
      - A single dropped connection shouldn't lose the remote plan; a down service must
        not block answers.

    **What it tests:**
      - ``UNAVAILABLE`` twice → two transport calls, then the local plan with
        ``planner_fail_open``
    """
    transport = _transport(UnavailableError("overlord down"))
    planner, catalog = _planner(catalog_raw, routes_raw, transport)

    result = await planner.plan(_CTX, _input(catalog))

    assert transport.route.await_count == 2
    assert (result.decider, result.reason) == ("local", "planner_fail_open")


@pytest.mark.asyncio
async def test_plan_naming_unoffered_endpoint_is_rejected(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a plan naming a model the request didn't offer is discarded.

    **Why this test is important:**
      - The filter enforced the tenant's data policy; a remote plan must not route around
        it.

    **What it tests:**
      - with only Sonnet offered, a plan for ``meta/small`` fails open to the local plan,
        counted with ``cause=routing_plan_invalid``
    """
    counters: dict[str, MagicMock] = {}
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(
        side_effect=lambda name, *_a: counters.setdefault(name, MagicMock())
    )
    planner, catalog = _planner(
        catalog_raw,
        routes_raw,
        _transport(RoutingPlan(model_id="meta/small")),
        metrics=metrics,
    )
    sonnet = tuple(ref for ref in catalog.refs if ref.model_id == "anthropic/sonnet")

    result = await planner.plan(_CTX, PlanInput(query="q", candidates=sonnet))

    assert {ref.model_id for ref in result.ordered} == {"anthropic/sonnet"}
    assert result.reason == "planner_fail_open"
    counters["routed_planner_fail_open_total"].inc.assert_called_once_with(
        1.0, cause="routing_plan_invalid"
    )


@pytest.mark.asyncio
async def test_planner_attaches_service_token(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that every route call carries the caller's service token.

    And the request facts.

    **Why this test is important:**
      - The overlord rejects an unauthenticated caller; the token is how it knows
        retrieval asked.

    **What it tests:**
      - the metadata is ``authorization: Bearer tok``; the timeout is 0.05 s
      - the request carries the tenant, workspace, step, query and the offered candidates
    """
    transport = _transport(RoutingPlan(model_id="anthropic/sonnet"))
    planner, catalog = _planner(catalog_raw, routes_raw, transport)

    await planner.plan(_CTX, _input(catalog))

    request = transport.route.await_args.args[0]
    kwargs = transport.route.await_args.kwargs
    assert kwargs == {"timeout_s": 0.05, "metadata": (("authorization", "Bearer tok"),)}
    assert (request.org_id, request.workspace_id, request.step) == (
        "org-1",
        "ws-1",
        "generate",
    )
    assert request.candidates == catalog.refs


def test_planner_kind_overlord_requires_target(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the overlord kind fails at boot without a target or a transport.

    **Why this test is important:**
      - A misconfigured overlord would fail open on every request, silently; boot is where
        it must surface.

    **What it tests:**
      - an empty ``target`` and a missing transport each raise ``INVALID_INPUT``
    """
    with pytest.raises(AppError) as no_target:
        _planner(
            catalog_raw,
            routes_raw,
            _transport(),
            raw={"kind": "overlord", "overlord": {"target": ""}},
        )
    with pytest.raises(AppError) as no_transport:
        _planner(catalog_raw, routes_raw, None)

    assert no_target.value.code is ErrorCode.INVALID_INPUT
    assert no_transport.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_overlord_planner_sends_the_team_and_user(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the plan request carries the routing context's team and user.

    **Why this test is important:**
      - The remote planner records each routing decision for chargeback and analysis;
        without the team and user it can attribute a decision only to the tenant.

    **What it tests:**
      - a context with a team and a user → the transport's ``PlanRequest`` carries both
      - a context without them → the request's team and user are empty
    """
    plan = RoutingPlan(task_class="lookup", model_id="anthropic/haiku")
    transport = _transport(plan)
    planner, catalog = _planner(catalog_raw, routes_raw, transport)
    attributed = RouteContext(
        tenant_id="org-1",
        workspace_id="ws-1",
        step="generate",
        team_id="team-1",
        user_id="user-1",
    )

    _ = await planner.plan(attributed, _input(catalog))
    _ = await planner.plan(_CTX, _input(catalog))

    sent = [call.args[0] for call in transport.route.await_args_list]
    assert [(r.team_id, r.user_id) for r in sent] == [("team-1", "user-1"), ("", "")]
