"""Unit tests for shadow routing: an alternative plan computed alongside the live one."""

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
from techai_webutils.core.interfaces.fact_publisher import FactPublisher
from techai_webutils.core.interfaces.llm import LLMMessage
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.foundation.tokens.cost import ModelPrice, PriceTable

_PRICES = PriceTable(
    prices={
        model: ModelPrice(Decimal(price), Decimal(price), Decimal(price))
        for model, price in (
            ("anthropic/sonnet", 3),
            ("anthropic/haiku", 1),
            ("meta/small", 0),
        )
    },
    version="test",
)


@pytest.mark.asyncio
async def test_shadow_run_makes_no_model_call_and_emits_shadow_fact(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a shadow policy plans alongside the live one without serving anything.

    **Why this test is important:**
      - Shadow routing is how a new policy is compared before cutover; it must never call
        a model or delay the live answer.

    **What it tests:**
      - the live call is served by Sonnet (static order) while a ``cost_aware``/``cost``
        shadow plans the small model
      - exactly one model call is made (the live one)
      - two facts are published: the live decision and a ``shadow=true`` one naming the
        shadow's choice
    """
    facts = MagicMock(spec=FactPublisher)
    built: list[StubLlmProvider] = []

    def factory(llm: Any) -> StubLlmProvider:  # noqa: ANN401 - LlmConfig
        provider = StubLlmProvider(llm.model)
        built.append(provider)
        return provider

    ctx = RouteContext(tenant_id="org-1", step="generate", slo="cost")
    gateway = new_model_gateway_from_config(
        GatewayConfig(
            kind=GatewayKind.ROUTED,
            routed=RoutedConfig(
                catalog=catalog_raw,
                routes=routes_raw,
                sink={"kind": "analytics"},
                shadow={"ranker": {"kind": "cost_aware"}},
                policy_store={"policies": {"org-1": {"allow_off_cloud": True}}},
            ),
        ),
        provider_factory=factory,
        context_provider=lambda: ctx,
        prices=_PRICES,
        facts=facts,
    )

    async with gateway:
        response = await gateway.complete([LLMMessage(role="user", content="q")])

    shadow_flags = [
        call.args[0][0].dims["shadow"] for call in facts.publish.call_args_list
    ]
    shadow_fact = next(
        call.args[0][0]
        for call in facts.publish.call_args_list
        if call.args[0][0].dims["shadow"] == "true"
    )
    assert response.model == "sonnet-use1"
    assert [p.model_name() for p in built] == ["sonnet-use1"]
    assert sorted(shadow_flags) == ["false", "true"]
    assert shadow_fact.dims["model_id"] == "meta/small"
