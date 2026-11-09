"""Unit tests for ``RoutedGateway``: filter → plan → execute over the catalog."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Any
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.model_gateway import (
    GatewayConfig,
    GatewayKind,
    RoutedConfig,
    new_model_gateway_from_config,
)
from techai_webutils.clients.model_gateway.builder import ModelGatewayDependencies
from techai_webutils.clients.model_gateway.routed import RoutedGateway
from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.clients.model_gateway.routed.gateway import RoutedGatewayDependencies
from techai_webutils.clients.model_gateway.routed.policy_store import (
    routing_policy_store_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.llm import LLMMessage, LLMProvider
from techai_webutils.core.interfaces.model_gateway import (
    ModelGateway,
    RouteContext,
    current_route_decision,
)
from techai_webutils.core.interfaces.routing import RoutePlanner, RoutingPolicyStore
from techai_webutils.core.types.routing import RoutingPolicy

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.types.llm import LlmConfig

_MESSAGES = [LLMMessage(role="user", content="what is the refund window")]


def _gateway(
    catalog_raw: dict[str, Any],
    routes_raw: dict[str, Any],
    ctx: RouteContext,
    *,
    policies: dict[str, Any] | None = None,
    factory: Callable[[LlmConfig], LLMProvider] | None = None,
) -> RoutedGateway:
    """Build a routed gateway over the fixture catalog whose context is always ``ctx``.

    Returns:
        A routed gateway over the fixture catalog whose context is always ``ctx``.

    """
    config = GatewayConfig(
        kind=GatewayKind.ROUTED,
        routed=RoutedConfig(
            catalog=catalog_raw,
            routes=routes_raw,
            policy_store={"kind": "config", "policies": policies or {}},
        ),
    )
    gateway = new_model_gateway_from_config(
        config,
        options=ModelGatewayDependencies(
            provider_factory=factory or (lambda llm: StubLlmProvider(llm.model)),
            context_provider=lambda: ctx,
            priced_ids=None,
        ),
    )
    assert isinstance(gateway, RoutedGateway)
    return gateway


@pytest.mark.asyncio
async def test_routed_default_stages_match_passthrough(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that routed with default stages and no pins serves each step's model as today.

    **Why this test is important:**
      - ``routed`` ships dark: selecting it with the defaults must change no step's model.

    **What it tests:**
      - ``resolve`` gives ``haiku-use1`` for ``rewrite`` and ``sonnet-use1`` for
        ``generate``
      - a served ``complete`` answers from Sonnet and records route ``generate``, decider
        ``local``, mode ``user``, one ``ok`` attempt and the token count
    """
    gateway = _gateway(
        catalog_raw, routes_raw, RouteContext(tenant_id="org-1", step="generate")
    )

    response = await gateway.complete(_MESSAGES)
    decision = current_route_decision()

    assert (
        await gateway.resolve(RouteContext(step="rewrite"))
    ).model_name() == "haiku-use1"
    assert (
        await gateway.resolve(RouteContext(step="generate"))
    ).model_name() == "sonnet-use1"
    assert response.model == "sonnet-use1"
    assert decision is not None
    assert (decision.route, decision.decider, decision.mode) == (
        "generate",
        "local",
        "user",
    )
    assert (decision.model_id, decision.endpoint) == ("anthropic/sonnet", "use1")
    assert [(a.endpoint, a.outcome) for a in decision.attempts] == [
        ("anthropic/sonnet@use1", "ok")
    ]
    assert decision.tokens == response.input_tokens + response.output_tokens
    assert gateway.model_name() == "sonnet-use1"


@pytest.mark.asyncio
async def test_pin_resolves_model_and_skips_classify_and_rank(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a tenant pin serves its model without consulting the planner.

    **Why this test is important:**
      - A pin is the old static table: an admin's word, decided before any classifier
        cost.

    **What it tests:**
      - with ``generate`` pinned to Haiku, the call is served by Haiku, decider
        ``admin_pin``, reason ``pinned``, and the planner's ``plan`` is never awaited
    """
    catalog = ModelCatalog.from_mapping(catalog_raw, priced_ids=None)
    planner = MagicMock(spec=RoutePlanner)
    planner.plan = AsyncMock()
    gateway = RoutedGateway(
        options=RoutedGatewayDependencies(
            catalog=catalog,
            routes=parse_routes(routes_raw, catalog),
            default_route="",
            policy_store=routing_policy_store_from_config({
                "policies": {"org-1": {"pins": {"generate": "anthropic/haiku"}}}
            }),
            planner=planner,
            provider_factory=lambda llm: StubLlmProvider(llm.model),
            context_provider=lambda: RouteContext(tenant_id="org-1", step="generate"),
        )
    )

    response = await gateway.complete(_MESSAGES)
    decision = current_route_decision()

    planner.plan.assert_not_awaited()
    assert response.model == "haiku-use1"
    assert decision is not None
    assert (decision.decider, decision.reason) == ("admin_pin", "pinned")


@pytest.mark.asyncio
async def test_routed_reuses_endpoint_instances(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that each endpoint's provider is built once and reused.

    **Why this test is important:**
      - A provider owns an SDK client; building one per call leaks connections.

    **What it tests:**
      - two resolves of the same step return the same instance; the factory runs once per
        distinct endpoint; streaming a call records its decision once the stream ends
      - closing the gateway closes each built provider
    """
    built: list[StubLlmProvider] = []

    def factory(llm: LlmConfig) -> StubLlmProvider:
        provider = StubLlmProvider(llm.model)
        provider.__aexit__ = AsyncMock(return_value=None)  # type: ignore[method-assign]
        built.append(provider)
        return provider

    gateway = _gateway(
        catalog_raw, routes_raw, RouteContext(step="rewrite"), factory=factory
    )

    first = await gateway.resolve(RouteContext(step="rewrite"))
    second = await gateway.resolve(RouteContext(step="rewrite"))
    await gateway.resolve(RouteContext(step="generate"))

    async def consume() -> object:
        _ = [c async for c in await gateway.stream(_MESSAGES)]
        return current_route_decision()

    decision = await asyncio.create_task(consume())
    async with gateway:
        pass

    assert first is second
    assert [p.model_name() for p in built] == ["haiku-use1", "sonnet-use1"]
    assert decision is not None
    for provider in built:
        provider.__aexit__.assert_awaited_once()  # type: ignore[attr-defined]


@pytest.mark.asyncio
async def test_route_context_defaults_when_provider_returns_empty_context(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that an empty request context still routes, to the default route.

    **Why this test is important:**
      - Background calls (an eval run, a warm-up) carry no tenant; they must not fail.

    **What it tests:**
      - with ``RouteContext()`` and ``default_route=generate``, the call is served by
        Sonnet under the default policy
      - the gateway's ``wrapped_provider`` is a built catalog provider (for span
        attributes)
    """
    config = GatewayConfig(
        kind=GatewayKind.ROUTED,
        routed=RoutedConfig(
            catalog=catalog_raw, routes=routes_raw, default_route="generate"
        ),
    )
    gateway = new_model_gateway_from_config(
        config,
        options=ModelGatewayDependencies(
            provider_factory=lambda llm: StubLlmProvider(llm.model),
            context_provider=RouteContext,
        ),
    )

    response = await gateway.complete(_MESSAGES)

    assert response.model == "sonnet-use1"
    assert isinstance(gateway.wrapped_provider, StubLlmProvider)  # type: ignore[attr-defined]


@pytest.mark.asyncio
async def test_gateway_warms_embedding_classifier_on_enter(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that entering the gateway embeds the route exemplars.

    And fails the boot if it can't.

    **Why this test is important:**
      - Embedding exemplars per request would add a batch call to every answer; an
        embedder that is down at boot must surface then, not as silently degraded routing.

    **What it tests:**
      - ``async with`` embeds the exemplars once, as queries, in one ``embed_batch``
      - an ``embed_batch`` that raises makes entering the gateway raise ``UNAVAILABLE``
    """
    routes = {
        **routes_raw,
        "generate": {**routes_raw["generate"], "exemplars": ["summarize"]},
    }
    embedding = MagicMock(spec=EmbeddingProvider)
    embedding.embed_batch = AsyncMock(
        return_value=[EmbeddingResult(embedding=[1.0, 0.0], model="e", token_count=1)]
    )
    broken = MagicMock(spec=EmbeddingProvider)
    broken.embed_batch = AsyncMock(side_effect=RuntimeError("embedder down"))

    def build(embedder: MagicMock) -> ModelGateway:
        return new_model_gateway_from_config(
            GatewayConfig(
                kind=GatewayKind.ROUTED,
                routed=RoutedConfig(
                    catalog=catalog_raw, routes=routes, classifier={"kind": "embedding"}
                ),
            ),
            options=ModelGatewayDependencies(
                provider_factory=lambda llm: StubLlmProvider(llm.model),
                embedding=embedder,
            ),
        )

    async with build(embedding):
        pass
    with pytest.raises(AppError) as caught:
        async with build(broken):
            pass

    embedding.embed_batch.assert_awaited_once_with(["summarize"], input_type="query")
    assert caught.value.code is ErrorCode.UNAVAILABLE


@pytest.mark.asyncio
async def test_transient_failure_fails_over_and_records_reason(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a throttled primary fails over and the decision says so.

    **Why this test is important:**
      - Failover is invisible in the answer; the decision is the only record that the
        primary region degraded.

    **What it tests:**
      - Sonnet ``use1`` raising ``UNAVAILABLE`` is served by Sonnet ``usw2``
      - the decision's reason is ``failover`` with attempts ``failed`` then ``ok``
    """

    def factory(llm: LlmConfig) -> StubLlmProvider:
        provider = StubLlmProvider(llm.model)
        if llm.model == "sonnet-use1":
            provider.complete = AsyncMock(side_effect=UnavailableError("throttled"))  # type: ignore[method-assign]
        return provider

    gateway = _gateway(
        catalog_raw,
        routes_raw,
        RouteContext(tenant_id="org-1", step="generate"),
        factory=factory,
    )

    response = await gateway.complete(_MESSAGES)
    decision = current_route_decision()

    assert response.model == "sonnet-usw2"
    assert decision is not None
    assert (decision.reason, decision.endpoint) == ("failover", "usw2")
    assert [a.outcome for a in decision.attempts] == ["failed", "ok"]


@pytest.mark.asyncio
async def test_injected_policy_store_replaces_the_configured_one(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a caller-supplied policy store is the one the routed gateway consults.

    **Why this test is important:**
      - A service that keeps policies in a database hands the gateway a refreshing store;
        if the gateway kept reading ``config.policy_store`` the stored policies would
        never apply, while a gateway built without one must still read its configuration.

    **What it tests:**
      - with an injected store pinning ``generate`` to Haiku, the call is served by Haiku
        even though the configured store pins nothing
      - without one, the configured store's pin applies
    """
    ctx = RouteContext(tenant_id="org-1", step="generate")
    store = MagicMock(spec=RoutingPolicyStore)
    store.policy.return_value = RoutingPolicy(pins={"generate": "anthropic/haiku"})
    config = GatewayConfig(
        kind=GatewayKind.ROUTED,
        routed=RoutedConfig(catalog=catalog_raw, routes=routes_raw),
    )
    injected = new_model_gateway_from_config(
        config,
        options=ModelGatewayDependencies(
            provider_factory=lambda llm: StubLlmProvider(llm.model),
            context_provider=lambda: ctx,
            policy_store=store,
        ),
    )
    configured = _gateway(
        catalog_raw,
        routes_raw,
        ctx,
        policies={"org-1": {"pins": {"generate": "anthropic/haiku"}}},
    )

    assert (await injected.complete(_MESSAGES)).model == "haiku-use1"
    store.policy.assert_called_with("org-1", "")
    assert (await configured.complete(_MESSAGES)).model == "haiku-use1"


def test_cost_aware_gateway_without_prices_fails_at_boot(
    catalog_raw: dict[str, Any], routes_raw: dict[str, Any]
) -> None:
    """Test that a routed gateway ranking by cost refuses to boot with no price table.

    **Why this test is important:**
      - The gateway ranks with the one table it is given at boot; without it every request
        would fail, so the defect must stop the boot instead of the first call.

    **What it tests:**
      - ``ranker.kind: cost_aware`` with no ``prices`` raises ``AppError(INVALID_INPUT)``
    """
    config = GatewayConfig(
        kind=GatewayKind.ROUTED,
        routed=RoutedConfig(
            catalog=catalog_raw, routes=routes_raw, ranker={"kind": "cost_aware"}
        ),
    )

    with pytest.raises(AppError) as excinfo:
        _ = new_model_gateway_from_config(
            config,
            options=ModelGatewayDependencies(
                provider_factory=lambda llm: StubLlmProvider(llm.model), priced_ids=None
            ),
        )

    assert excinfo.value.code == ErrorCode.INVALID_INPUT
