"""``RoutedGateway`` — per-request routing over a model catalog: filter → plan → execute.

1. **Filter** (deterministic): capability, data policy, deny and allow lists, and an explicit
   choice (the end user's, or a pin) — see ``filter.py``.
2. **Plan**: a direct choice skips it; otherwise the configured ``RoutePlanner`` (``local``:
   classify, then rank) orders the survivors.
3. **Execute**: the first endpoint of the plan serves. Each distinct endpoint's provider is built
   once through the injected ``provider_factory`` and reused; closing the gateway closes them.

Request context comes from the injected ``context_provider``; the prompt size is estimated as
characters / 4. Every served call records a ``RouteDecision`` (a stream, once fully consumed).
"""

from __future__ import annotations

from dataclasses import replace
from typing import TYPE_CHECKING, Self, override

from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.clients.model_gateway.routed.classifier import (
    route_classifier_from_config,
)
from techai_webutils.clients.model_gateway.routed.filter import filter_candidates
from techai_webutils.clients.model_gateway.routed.planner import route_planner_from_config
from techai_webutils.clients.model_gateway.routed.policy_store import (
    routing_policy_store_from_config,
)
from techai_webutils.clients.model_gateway.routed.ranker import (
    candidate_ranker_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import (
    ModelGateway,
    RouteAttempt,
    RouteDecision,
    set_route_decision,
)
from techai_webutils.core.types.routing import PlanInput, RouteRequirements

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable, Mapping

    from techai_webutils.clients.llm.builder import LlmConfig
    from techai_webutils.clients.model_gateway.builder import RoutedConfig
    from techai_webutils.core.interfaces.llm import (
        LLMConfig,
        LLMMessage,
        LLMProvider,
        LLMResponse,
        StreamUsage,
    )
    from techai_webutils.core.interfaces.model_gateway import (
        RouteContext,
        RouteContextProvider,
    )
    from techai_webutils.core.interfaces.routing import RoutePlanner, RoutingPolicyStore
    from techai_webutils.core.types.routing import CatalogEndpointRef, Route

_CHARS_PER_TOKEN = 4
"""The prompt-size estimate's characters per token (a documented approximation)."""


def _requirements(
    messages: list[LLMMessage], config: LLMConfig | None, *, streaming: bool
) -> RouteRequirements:
    """Estimate what a call needs from its messages and per-call config."""
    chars = sum(len(message.content) for message in messages)
    return RouteRequirements(
        prompt_tokens=chars // _CHARS_PER_TOKEN,
        max_tokens=config.max_tokens if config is not None else 0,
        streaming=streaming,
    )


def _query(messages: list[LLMMessage]) -> str:
    """Return the last user message (what a classifier reads)."""
    return next((m.content for m in reversed(messages) if m.role == "user"), "")


async def _recorded[T](
    items: AsyncIterator[T], decision: RouteDecision
) -> AsyncIterator[T]:
    """Re-yield ``items`` and record ``decision`` (with streamed usage) once exhausted."""
    tokens = 0
    async for item in items:
        usage_tokens = getattr(item, "input_tokens", None)
        if usage_tokens is not None:
            tokens = int(usage_tokens) + int(getattr(item, "output_tokens", 0))
        yield item
    set_route_decision(replace(decision, tokens=tokens))


class RoutedGateway(ModelGateway):
    """A gateway that chooses, per request, which catalog endpoint serves."""

    def __init__(
        self,
        *,
        catalog: ModelCatalog,
        routes: Mapping[str, Route],
        default_route: str,
        policy_store: RoutingPolicyStore,
        planner: RoutePlanner,
        provider_factory: Callable[[LlmConfig], LLMProvider],
        context_provider: RouteContextProvider,
    ) -> None:
        """Bind the parsed catalog, routes, policy store, planner and collaborators."""
        self._catalog = catalog
        self._routes = routes
        self._default_route = default_route
        self._policies = policy_store
        self._planner = planner
        self._provider_factory = provider_factory
        self._context = context_provider
        self._providers: dict[str, LLMProvider] = {}

    @classmethod
    def from_config(
        cls,
        config: RoutedConfig,
        *,
        provider_factory: Callable[[LlmConfig], LLMProvider],
        context_provider: RouteContextProvider,
        priced_ids: frozenset[str] | None,
    ) -> RoutedGateway:
        """Parse and validate ``config`` and build its stages (no provider is built yet).

        Raises:
            AppError: ``INVALID_INPUT`` for a defective catalog, route or policy.
            AppValueError: ``INVALID_INPUT`` for an unknown stage kind.

        """
        catalog = ModelCatalog.from_mapping(config.catalog, priced_ids=priced_ids)
        routes = parse_routes(config.routes, catalog)
        if config.default_route and config.default_route not in routes:
            msg = f"default route {config.default_route!r} is not a configured route"
            raise AppError(ErrorCode.INVALID_INPUT, msg)
        classifier = route_classifier_from_config(
            config.classifier,
            route_names=frozenset(routes),
            default_route=config.default_route,
        )
        planner = route_planner_from_config(
            config.planner,
            classifier=classifier,
            ranker=candidate_ranker_from_config(config.ranker),
            routes=routes,
        )
        return cls(
            catalog=catalog,
            routes=routes,
            default_route=config.default_route,
            policy_store=routing_policy_store_from_config(config.policy_store),
            planner=planner,
            provider_factory=provider_factory,
            context_provider=context_provider,
        )

    def _provider(self, ref: CatalogEndpointRef) -> LLMProvider:
        """Return ``ref``'s provider, building it on first use."""
        provider = self._providers.get(ref.key)
        if provider is None:
            provider = self._provider_factory(self._catalog.llm_config(ref))
            self._providers[ref.key] = provider
        return provider

    @property
    def wrapped_provider(self) -> LLMProvider:
        """The catalog's first endpoint's provider (identifies the provider family for spans)."""
        return self._provider(self._catalog.refs[0])

    async def _decide(
        self, ctx: RouteContext, query: str, req: RouteRequirements
    ) -> tuple[CatalogEndpointRef, RouteDecision]:
        """Filter and plan ``ctx``'s request; return the serving endpoint and its decision."""
        policy = self._policies.policy(ctx.tenant_id, ctx.workspace_id)
        filtered = filter_candidates(self._catalog.refs, policy, ctx, req)
        rejections = dict(filtered.rejections)
        if filtered.direct_model is not None:
            ordered = filtered.kept
            route, confidence, decider = "", 1.0, filtered.decider
            reason = "pinned" if decider == "admin_pin" else "user_choice"
        else:
            plan = await self._planner.plan(
                ctx, PlanInput(query=query, candidates=filtered.kept, requirements=req)
            )
            ordered, route, confidence = plan.ordered, plan.route, plan.confidence
            decider, reason = plan.decider, plan.reason
            rejections.update(plan.rejections)
        if not ordered:
            raise AppError(ErrorCode.FORBIDDEN, "routing plan left no endpoint")
        first = ordered[0]
        decision = RouteDecision(
            route=route,
            confidence=confidence,
            model_id=first.model_id,
            endpoint=first.endpoint,
            candidates=tuple(ref.key for ref in ordered),
            rejections=rejections,
            reason=reason,
            mode=policy.mode,
            decider=decider,
        )
        return first, decision

    async def _route(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, streaming: bool
    ) -> tuple[LLMProvider, RouteDecision]:
        """Return the provider that serves this call and the decision to record."""
        req = _requirements(messages, config, streaming=streaming)
        ref, decision = await self._decide(self._context(), _query(messages), req)
        decision = replace(
            decision, attempts=(RouteAttempt(endpoint=ref.key, outcome="ok"),)
        )
        return self._provider(ref), decision

    @override
    async def resolve(self, ctx: RouteContext) -> LLMProvider:
        """Return the provider the first endpoint of ``ctx``'s plan uses."""
        ref, _ = await self._decide(ctx, "", RouteRequirements())
        return self._provider(ref)

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Route, then complete on the chosen endpoint."""
        provider, decision = await self._route(messages, config, streaming=False)
        response = await provider.complete(messages, config)
        tokens = response.input_tokens + response.output_tokens
        set_route_decision(replace(decision, tokens=tokens))
        return response

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Route, then stream from the chosen endpoint."""
        provider, decision = await self._route(messages, config, streaming=True)
        return _recorded(await provider.stream(messages, config), decision)

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Route, then stream with usage from the chosen endpoint."""
        provider, decision = await self._route(messages, config, streaming=True)
        return _recorded(await provider.stream_with_usage(messages, config), decision)

    @override
    def model_name(self) -> str:
        """Return the current step's default model (policy and planning are not consulted)."""
        step = self._context().step
        route = self._routes.get(step) or self._routes.get(self._default_route)
        if route is None:
            return self.wrapped_provider.model_name()
        ref = next(r for r in self._catalog.refs if r.model_id == route.default_model)
        return self._provider(ref).model_name()

    async def __aenter__(self) -> Self:
        """Enter the gateway's context; providers open lazily as they are built."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Close every provider this gateway built."""
        for provider in self._providers.values():
            await provider.__aexit__(None, None, None)
        self._providers.clear()


_INTERFACE_CHECK: type[ModelGateway] = RoutedGateway
"""Type-checker assertion that ``RoutedGateway`` satisfies ``ModelGateway``."""
