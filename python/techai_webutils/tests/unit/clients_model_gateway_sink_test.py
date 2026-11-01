"""Unit tests for route-decision sinks and the routed gateway's finalized decision."""

from __future__ import annotations

from decimal import Decimal
from typing import Any
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.model_gateway import (
    GatewayConfig,
    GatewayKind,
    RoutedConfig,
    new_model_gateway_from_config,
)
from techai_webutils.clients.model_gateway.routed.sink import (
    route_decision_sink_from_config,
)
from techai_webutils.clients.token_ledger.cost import ModelPrice, PriceTable
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.llm import LLMMessage
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.interfaces.model_gateway import (
    RouteContext,
    RouteDecision,
    current_route_decision,
)

_PRICES = PriceTable(
    prices={
        "anthropic/sonnet": ModelPrice(Decimal("0.003"), Decimal("0.015"), Decimal(0)),
        "anthropic/haiku": ModelPrice(Decimal("0.001"), Decimal("0.005"), Decimal(0)),
        "meta/small": ModelPrice(Decimal("0.0001"), Decimal("0.0002"), Decimal(0)),
    },
    version="test",
)
_CTX = RouteContext(tenant_id="org-1", workspace_id="ws-1", step="generate")


def test_analytics_sink_publishes_a_route_decisions_fact() -> None:
    """Test that the analytics sink turns a decision into one ``route_decisions`` fact.

    **Why this test is important:**
      - Routing quality and savings are analysed from these facts; a missing field blinds the
        chart that compares routing policies.

    **What it tests:**
      - one fact for cube ``route_decisions``, org ``org-1``, with the route, model, decider and
        shadow flag as dims and attempts, tokens and cost as measures
      - the ``noop`` default publishes nothing; an unknown kind is ``INVALID_INPUT``
    """
    facts = MagicMock(spec=FactPublisher)
    sink = route_decision_sink_from_config({"kind": "analytics"}, facts=facts)
    decision = RouteDecision(
        route="generate",
        model_id="anthropic/sonnet",
        endpoint="use1",
        decider="local",
        tokens=12,
        actual_cost=Decimal("0.0001"),
    )

    sink.record(decision, _CTX)
    route_decision_sink_from_config({}, facts=facts).record(decision, _CTX)

    (published,) = facts.publish.call_args.args[0]
    assert (published.cube, published.org_id) == ("route_decisions", "org-1")
    assert published.dims["model_id"] == "anthropic/sonnet"
    assert published.dims["shadow"] == "false"
    assert published.measures["tokens"] == pytest.approx(12.0)
    facts.publish.assert_called_once()
    with pytest.raises(AppError) as caught:
        route_decision_sink_from_config({"kind": "kafka"})
    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_decision_carries_actual_tokens_and_cost(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a served call's decision carries its estimate, tokens and cost.

    **Why this test is important:**
      - The decision is the routing audit trail: what was expected, what it cost.

    **What it tests:**
      - after a ``complete`` the decision has ``tokens`` = input + output, ``actual_cost`` from
        the price table and an ``estimated_cost``
      - the decision is handed to the sink with the request's context
    """
    facts = MagicMock(spec=FactPublisher)
    gateway = new_model_gateway_from_config(
        GatewayConfig(
            kind=GatewayKind.ROUTED,
            routed=RoutedConfig(
                catalog=catalog_raw, routes=routes_raw, sink={"kind": "analytics"}
            ),
        ),
        provider_factory=lambda llm: StubLlmProvider(llm.model),
        context_provider=lambda: _CTX,
        prices=_PRICES,
        facts=facts,
    )

    response = await gateway.complete([
        LLMMessage(role="user", content="how long is the window")
    ])
    decision = current_route_decision()

    assert decision is not None
    assert decision.tokens == response.input_tokens + response.output_tokens
    expected = (
        Decimal(response.input_tokens) * Decimal("0.003")
        + Decimal(response.output_tokens) * Decimal("0.015")
    ) / 1000
    assert decision.actual_cost == expected.quantize(Decimal("0.000001"))
    assert decision.estimated_cost is not None
    (fact,) = facts.publish.call_args.args[0]
    assert fact.dims["workspace_id"] == "ws-1"


@pytest.mark.asyncio
async def test_sink_failure_never_fails_the_answer(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a sink that raises is counted, never surfaced.

    **Why this test is important:**
      - Decision facts are telemetry; their outage must not fail an answer.

    **What it tests:**
      - with a publisher that raises, ``complete`` still answers
      - ``route_decision_sink_failures_total`` is incremented
    """
    facts = MagicMock(spec=FactPublisher)
    facts.publish = MagicMock(side_effect=RuntimeError("kafka down"))
    failures = MagicMock(spec=MetricCounter)
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter = MagicMock(return_value=failures)
    gateway = new_model_gateway_from_config(
        GatewayConfig(
            kind=GatewayKind.ROUTED,
            routed=RoutedConfig(
                catalog=catalog_raw, routes=routes_raw, sink={"kind": "analytics"}
            ),
        ),
        provider_factory=lambda llm: StubLlmProvider(llm.model),
        context_provider=lambda: _CTX,
        facts=facts,
        metrics=metrics,
    )

    response = await gateway.complete([LLMMessage(role="user", content="q")])

    assert response.content == "q"
    failures.inc.assert_called()
