"""Unit tests for the ``cost_aware`` candidate ranker."""

from __future__ import annotations

from decimal import Decimal
from typing import Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.clients.model_gateway.routed.ranker import (
    candidate_ranker_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.interfaces.token_ledger import (
    BudgetDecision,
    Period,
    TokenLedger,
    UsageScope,
    UsageSummary,
)
from techai_webutils.core.types.routing import (
    PlanInput,
    Route,
    RouteRequirements,
    RoutingPolicy,
)
from techai_webutils.foundation.tokens.cost import ModelPrice, PriceTable

_PRICES = PriceTable(
    prices={
        "anthropic/sonnet": ModelPrice(Decimal("0.003"), Decimal("0.015"), Decimal(0)),
        "anthropic/haiku": ModelPrice(Decimal("0.001"), Decimal("0.005"), Decimal(0)),
        "meta/small": ModelPrice(Decimal("0.0001"), Decimal("0.0002"), Decimal(0)),
    },
    version="test",
)
_REQ = RouteRequirements(prompt_tokens=1000, max_tokens=1000)
_GEN = Route("generate", "anthropic/sonnet")


def _catalog(catalog_raw: dict[str, Any]) -> ModelCatalog:
    """Return the fixture catalog."""
    return ModelCatalog.from_mapping(catalog_raw, priced_ids=None)


def _ledger(spent: str, remaining: str) -> MagicMock:
    """Return a ledger mock reporting ``spent`` dollars used and ``remaining`` left."""
    ledger = MagicMock(spec=TokenLedger)
    scope = UsageScope(org_id="org-1")
    ledger.usage = AsyncMock(
        return_value=UsageSummary(
            scope=scope, period=Period.MONTHLY, cost_usd=Decimal(spent)
        )
    )
    ledger.check_budget = AsyncMock(
        return_value=BudgetDecision(
            allowed=True,
            reason="within_budget",
            remaining_tokens=None,
            remaining_cost_usd=Decimal(remaining),
        )
    )
    return ledger


async def _rank(
    catalog: ModelCatalog,
    *,
    slo: str = "",
    policy: RoutingPolicy | None = None,
    route: Route = _GEN,
    ledger: MagicMock | None = None,
) -> tuple[list[str], dict[str, str]]:
    """Rank the catalog and return the ordered keys and the rejections."""
    ranker = candidate_ranker_from_config(
        {"kind": "cost_aware"}, prices=_PRICES, ledger=ledger
    )
    result = await ranker.rank(
        RouteContext(tenant_id="org-1", slo=slo),
        route,
        catalog.refs,
        PlanInput(
            query="q",
            candidates=catalog.refs,
            requirements=_REQ,
            policy=policy or RoutingPolicy(),
        ),
    )
    return [ref.key for ref in result.ordered], dict(result.rejections)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("slo", "first", "last"),
    [
        ("cost", "meta/small@local", "anthropic/sonnet@usw2"),
        ("latency", "meta/small@local", "anthropic/sonnet@usw2"),
        ("quality", "anthropic/sonnet@use1", "meta/small@local"),
        ("balanced", "anthropic/haiku@use1", None),
    ],
)
async def test_cost_aware_orders_by_objective(
    catalog_raw: dict[str, Any], slo: str, first: str, last: str | None
) -> None:
    """Test that each objective orders endpoints by its own measure.

    **Why this test is important:**
      - The objective is how a tenant trades cost for quality; each must actually reorder.

    **What it tests:**
      - ``cost`` and ``latency`` put the small model first; ``quality`` puts Sonnet first;
        ``balanced`` (0.5 quality, 0.3 cost, 0.2 latency, each min-max normalised) puts
        Haiku first (Sonnet and the small model tie behind it)
      - ties keep catalog order (``sonnet@use1`` before ``sonnet@usw2``)
    """
    ordered, _ = await _rank(_catalog(catalog_raw), slo=slo)

    assert ordered[0] == first
    if last is not None:
        assert ordered[-1] == last
    assert ordered.index("anthropic/sonnet@use1") < ordered.index("anthropic/sonnet@usw2")


@pytest.mark.asyncio
async def test_cost_aware_drops_candidates_over_hard_caps(
    catalog_raw: dict[str, Any],
) -> None:
    """Test that hard caps drop candidates outright and an empty result is forbidden.

    **Why this test is important:**
      - A cap is a tenant's promise of what a request may cost; ranking an over-cap model
        last would still call it once the others fail.

    **What it tests:**
      - a $0.01 cost cap drops both Sonnet endpoints (est. $0.018) as ``over_cap``
      - a 300 ms latency cap with the cost cap drops everything but the small model
      - caps that drop every candidate raise ``FORBIDDEN``
    """
    catalog = _catalog(catalog_raw)

    ordered, rejections = await _rank(
        catalog, policy=RoutingPolicy(max_cost_per_request=Decimal("0.01"))
    )
    tight, _ = await _rank(
        catalog,
        policy=RoutingPolicy(max_cost_per_request=Decimal("0.01"), max_latency_ms=300),
    )

    assert "anthropic/sonnet@use1" not in ordered
    assert rejections["anthropic/sonnet@use1"] == "over_cap"
    assert tight == ["meta/small@local"]
    with pytest.raises(AppError) as caught:
        await _rank(
            catalog, policy=RoutingPolicy(max_cost_per_request=Decimal("0.00001"))
        )
    assert caught.value.code is ErrorCode.FORBIDDEN


@pytest.mark.asyncio
async def test_cost_aware_biases_cheaper_when_near_budget(
    catalog_raw: dict[str, Any],
) -> None:
    """Test that a tenant near its budget is routed to cheaper endpoints.

    **Why this test is important:**
      - Spending the last 5% of a budget on the most expensive model ends the month early.

    **What it tests:**
      - a ``quality`` request at 95% of budget puts the cheapest endpoint first
      - at 50% of budget the same request keeps Sonnet first
      - a failing ledger means no bias (fail-open)
    """
    catalog = _catalog(catalog_raw)
    down = MagicMock(spec=TokenLedger)
    down.usage = AsyncMock(side_effect=RuntimeError("redis down"))
    down.check_budget = AsyncMock(side_effect=RuntimeError("redis down"))

    near, _ = await _rank(catalog, slo="quality", ledger=_ledger("95", "5"))
    far, _ = await _rank(catalog, slo="quality", ledger=_ledger("50", "50"))
    failing, _ = await _rank(catalog, slo="quality", ledger=down)

    assert near[0] == "meta/small@local"
    assert far[0] == "anthropic/sonnet@use1"
    assert failing[0] == "anthropic/sonnet@use1"


@pytest.mark.asyncio
async def test_cost_aware_drops_below_quality_floor_unless_degrade_on_budget(
    catalog_raw: dict[str, Any],
) -> None:
    """A route's quality floor drops weak models unless the tenant allows.

    Degrading.

    **Why this test is important:**
      - A synthesis route must not land on a model too weak to answer it, unless the
        tenant chose cost over quality.

    **What it tests:**
      - with ``quality_floor=0.6`` the small model (0.5) is dropped as ``quality_floor``
      - with ``degrade_on_budget`` it is kept
    """
    catalog = _catalog(catalog_raw)
    route = Route("synthesis", "anthropic/sonnet", quality_floor=0.6)

    strict, rejections = await _rank(catalog, route=route)
    lenient, _ = await _rank(
        catalog, route=route, policy=RoutingPolicy(degrade_on_budget=True)
    )

    assert "meta/small@local" not in strict
    assert rejections["meta/small@local"] == "quality_floor"
    assert "meta/small@local" in lenient


def test_routes_share_the_catalog(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that the fixture routes parse against the fixture catalog.

    (shared setup check).

    **Why this test is important:**
      - The ranker tests reuse the fixture catalog; a drift there would make them vacuous.

    **What it tests:**
      - the ``generate`` route's default is Sonnet
    """
    assert parse_routes(routes_raw, _catalog(catalog_raw))["generate"].default_model == (
        "anthropic/sonnet"
    )


@pytest.mark.asyncio
async def test_cost_aware_ranks_under_each_requests_own_prices(
    catalog_raw: dict[str, Any],
) -> None:
    """Test that one ranker ranks each request under the price table that request carries.

    **Why this test is important:**
      - A remote planner receives its candidates' prices with every request; building a
        new ranker per request to bind them re-reads the config on the hot path, and
        binding one table at boot would rank every tenant under the wrong prices.

    **What it tests:**
      - a ``cost_aware`` ranker built with no table orders by cost under the table passed
        to ``rank_priced``: the small model first under the fixture prices, Haiku first
        once a second table makes Haiku the cheapest, from the same ranker instance
    """
    catalog = _catalog(catalog_raw)
    ranker = candidate_ranker_from_config({"kind": "cost_aware"})
    cheap_haiku = PriceTable(
        prices={
            "anthropic/sonnet": ModelPrice(
                Decimal("0.003"), Decimal("0.015"), Decimal(0)
            ),
            "anthropic/haiku": ModelPrice(
                Decimal("0.00001"), Decimal("0.00001"), Decimal(0)
            ),
            "meta/small": ModelPrice(Decimal("0.01"), Decimal("0.02"), Decimal(0)),
        },
        version="request",
    )
    route_input = PlanInput(query="q", candidates=catalog.refs, requirements=_REQ)
    ctx = RouteContext(tenant_id="org-1", slo="cost")

    first = await ranker.rank_priced(ctx, _GEN, catalog.refs, route_input, _PRICES)
    second = await ranker.rank_priced(ctx, _GEN, catalog.refs, route_input, cheap_haiku)

    assert first.ordered[0].key == "meta/small@local"
    assert second.ordered[0].key == "anthropic/haiku@use1"


@pytest.mark.asyncio
async def test_cost_aware_without_a_table_refuses_an_unpriced_rank(
    catalog_raw: dict[str, Any],
) -> None:
    """Test that a ranker built without prices refuses ``rank``.

    Which has none to rank by.

    **Why this test is important:**
      - Ranking by cost with no prices would silently treat every model as unpriced and
        order them by accident; the caller must learn it forgot the table.

    **What it tests:**
      - ``rank`` on a ``cost_aware`` ranker with no construction table raises
        ``AppError(INVALID_INPUT)``
    """
    catalog = _catalog(catalog_raw)
    ranker = candidate_ranker_from_config({"kind": "cost_aware"})

    with pytest.raises(AppError) as excinfo:
        _ = await ranker.rank(
            RouteContext(),
            _GEN,
            catalog.refs,
            PlanInput(query="q", candidates=catalog.refs),
        )

    assert excinfo.value.code == ErrorCode.INVALID_INPUT
