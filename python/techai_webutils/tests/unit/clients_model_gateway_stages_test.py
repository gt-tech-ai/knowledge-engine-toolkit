"""Unit tests for the routed gateway's default stages and their ``Kind`` factories."""

from __future__ import annotations

from typing import Any

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
from techai_webutils.clients.token_ledger.cost import PriceTable
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.types.routing import Classification, PlanInput


@pytest.mark.asyncio
async def test_stub_classifier_returns_step_default_route(
    routes_raw: dict[str, Any],
) -> None:
    """Test that the stub classifier routes each step to its own route, else the default.

    **Why this test is important:**
      - With the stub classifier, routing must reproduce today's per-step model choice.

    **What it tests:**
      - step ``rewrite`` → route ``rewrite`` (confidence 1.0); an unrouted step → the
        default
      - with no default, an unrouted step raises ``INVALID_INPUT``
    """
    names = frozenset(routes_raw)
    classifier = route_classifier_from_config(
        {"kind": "stub"}, route_names=names, default_route="generate"
    )
    strict = route_classifier_from_config({}, route_names=names, default_route="")

    rewrite = await classifier.classify(RouteContext(step="rewrite"), "q")
    unrouted = await classifier.classify(RouteContext(step="summarize"), "q")

    assert rewrite == Classification(route="rewrite", confidence=1.0)
    assert unrouted == Classification(route="generate", confidence=1.0)
    with pytest.raises(AppError) as caught:
        await strict.classify(RouteContext(step="summarize"), "q")
    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_static_order_follows_route_preference_then_catalog(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the static ranker orders by the route's models, then catalog order.

    **Why this test is important:**
      - The default ranker is what keeps routing identical to today: the step's model
        first.

    **What it tests:**
      - the ``rewrite`` route puts Haiku first, then Sonnet's endpoints and the small
        model in catalog order; nothing is rejected
    """
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    routes = parse_routes(routes_raw, catalog)
    ranker = candidate_ranker_from_config({"kind": "static_order"})

    result = await ranker.rank(
        RouteContext(),
        routes["rewrite"],
        catalog.refs,
        PlanInput(query="q", candidates=catalog.refs),
    )

    assert [ref.key for ref in result.ordered] == [
        "anthropic/haiku@use1",
        "anthropic/sonnet@use1",
        "anthropic/sonnet@usw2",
        "meta/small@local",
    ]
    assert result.rejections == {}


@pytest.mark.asyncio
async def test_static_order_ignores_the_requests_prices(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the static ranker's per-request entry point ranks exactly as ``rank``.

    **Why this test is important:**
      - A caller that ranks with each request's prices may select ``static_order`` by
        config; it must keep the route's model order, never fail for the unused table.

    **What it tests:**
      - ``rank_priced`` with a price table returns the same order as ``rank``
    """
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    routes = parse_routes(routes_raw, catalog)
    ranker = candidate_ranker_from_config({"kind": "static_order"})
    route_input = PlanInput(query="q", candidates=catalog.refs)

    plain = await ranker.rank(
        RouteContext(), routes["rewrite"], catalog.refs, route_input
    )
    priced = await ranker.rank_priced(
        RouteContext(),
        routes["rewrite"],
        catalog.refs,
        route_input,
        PriceTable(prices={}, version="empty"),
    )

    assert priced.ordered == plain.ordered


@pytest.mark.asyncio
async def test_local_planner_classifies_then_ranks(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the local planner chains classifier and ranker into a plan.

    **Why this test is important:**
      - ``local`` is the planner every routed request uses until a remote planner exists.

    **What it tests:**
      - a ``generate`` request plans Sonnet first with route ``generate``, decider
        ``local``
    """
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    routes = parse_routes(routes_raw, catalog)
    planner = route_planner_from_config(
        {"kind": "local"},
        classifier=route_classifier_from_config(
            {}, route_names=frozenset(routes), default_route=""
        ),
        ranker=candidate_ranker_from_config({}),
        routes=routes,
    )

    plan = await planner.plan(
        RouteContext(step="generate"), PlanInput(query="q", candidates=catalog.refs)
    )

    assert (plan.route, plan.decider, plan.reason) == ("generate", "local", "planned")
    assert plan.ordered[0].key == "anthropic/sonnet@use1"


def test_planner_kind_unknown_raises(routes_raw: dict[str, Any]) -> None:
    """Test that every stage factory fails loudly on an unknown kind.

    **Why this test is important:**
      - A typo in a stage kind must stop boot, not run a different routing strategy.

    **What it tests:**
      - unknown planner, classifier and ranker kinds each raise a ``ValueError`` coded
        ``INVALID_INPUT``
    """
    classifier = route_classifier_from_config(
        {}, route_names=frozenset(), default_route=""
    )
    ranker = candidate_ranker_from_config({})
    builds = (
        lambda: route_planner_from_config(
            {"kind": "psychic"}, classifier=classifier, ranker=ranker, routes={}
        ),
        lambda: route_classifier_from_config(
            {"kind": "psychic"}, route_names=frozenset(routes_raw), default_route=""
        ),
        lambda: candidate_ranker_from_config({"kind": "psychic"}),
    )
    for build in builds:
        with pytest.raises(ValueError, match="kind") as caught:
            build()
        assert isinstance(caught.value, AppError)
        assert caught.value.code is ErrorCode.INVALID_INPUT
