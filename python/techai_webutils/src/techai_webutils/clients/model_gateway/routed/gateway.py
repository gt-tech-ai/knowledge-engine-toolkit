"""``RoutedGateway`` — per-request routing over a model catalog: filter → plan → execute.

1. **Filter** (deterministic): capability, data policy, deny and allow lists, and an
   explicit choice (the end user's, or a pin) — see ``filter.py``.
2. **Plan**: a direct choice skips it; otherwise the configured ``RoutePlanner``
   (``local``: classify, then rank) orders the survivors. A ``routed.shadow`` planner
   plans the same input in the background (``shadow.py``) and is never served.
3. **Execute**: ``ChainExecutor`` walks the plan — bounded attempts, a breaker per
   endpoint, failover on transient errors only (``executor.py``). Each distinct endpoint's
   provider is built once through the injected ``provider_factory`` and reused; closing
   the gateway drains the shadow and closes them.

Request context comes from the injected ``context_provider``; the prompt size is estimated
as characters / 4. Every served call finalizes a ``RouteDecision`` (a stream, once fully
consumed): attempts, served endpoint, tokens, estimated and actual cost (from the injected
price table). The decision is readable through ``current_route_decision()``, stamped on a
``route.decision`` span and handed to the injected ``RouteDecisionSink``; a sink failure
is counted (``route_decision_sink_failures_total``), never raised.
"""

from __future__ import annotations

from dataclasses import dataclass, replace
from typing import TYPE_CHECKING, Self, cast, final, override

from opentelemetry import trace

from techai_webutils.clients.model_gateway.routed.catalog import (
    ModelCatalog,
    parse_routes,
)
from techai_webutils.clients.model_gateway.routed.classifier import (
    route_classifier_from_config,
)
from techai_webutils.clients.model_gateway.routed.executor import ChainExecutor
from techai_webutils.clients.model_gateway.routed.filter import filter_candidates
from techai_webutils.clients.model_gateway.routed.planner import route_planner_from_config
from techai_webutils.clients.model_gateway.routed.planner.builder import (
    RoutePlannerDependencies,
)
from techai_webutils.clients.model_gateway.routed.policy_store import (
    routing_policy_store_from_config,
)
from techai_webutils.clients.model_gateway.routed.ranker import (
    CandidateRankerKind,
    candidate_ranker_from_config,
)
from techai_webutils.clients.model_gateway.routed.shadow import ShadowRouter
from techai_webutils.clients.model_gateway.routed.sink import (
    route_decision_sink_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import (
    ModelGateway,
    RouteDecision,
    set_route_decision,
)
from techai_webutils.core.types.routing import PlanInput, RouteRequirements
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.tokens.cost import estimate_cost

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Callable, Mapping, Sequence
    from decimal import Decimal

    from techai_webutils.clients.model_gateway.builder import RoutedConfig
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.llm import (
        LLMConfig,
        LLMMessage,
        LLMProvider,
        LLMResponse,
        StreamUsage,
    )
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.model_gateway import (
        RouteAttempt,
        RouteContext,
        RouteContextProvider,
    )
    from techai_webutils.core.interfaces.routing import (
        RouteClassifier,
        RouteDecisionSink,
        RoutePlanner,
        RoutingPolicyStore,
    )
    from techai_webutils.core.interfaces.token_ledger import TokenLedger
    from techai_webutils.core.types.llm import LlmConfig
    from techai_webutils.core.types.routing import CatalogEndpointRef, Plan, Route
    from techai_webutils.foundation.tokens.cost import PriceTable

logger = get_logger(__name__)

_CHARS_PER_TOKEN = 4
"""The prompt-size estimate's characters per token (a documented approximation)."""

_TRACER = trace.get_tracer(__name__)
"""Emits the ``route.decision`` span."""


def _requirements(
    messages: list[LLMMessage], config: LLMConfig | None, *, streaming: bool
) -> RouteRequirements:
    """Estimate what a call needs from its messages and per-call config.

    Returns:
        The requirements of the call.

    """
    chars = sum(len(message.content) for message in messages)
    return RouteRequirements(
        prompt_tokens=chars // _CHARS_PER_TOKEN,
        max_tokens=config.max_tokens if config is not None else 0,
        streaming=streaming,
    )


def _query(messages: list[LLMMessage]) -> str:
    """Return the last user message (what a classifier reads).

    Returns:
        The last user message (what a classifier reads).

    """
    return next((m.content for m in reversed(messages) if m.role == "user"), "")


def _plan_decision(
    plan: Plan, rejections: Mapping[str, str], mode: str
) -> RouteDecision | None:
    """Return the (unserved) decision ``plan`` describes; ``None`` for an empty plan.

    Returns:
        The (unserved) decision ``plan`` describes; ``None`` for an empty plan.

    """
    if not plan.ordered:
        return None
    first = plan.ordered[0]
    return RouteDecision(
        route=plan.route,
        confidence=plan.confidence,
        model_id=first.model_id,
        endpoint=first.endpoint,
        candidates=tuple(ref.key for ref in plan.ordered),
        rejections={**rejections, **plan.rejections},
        reason=plan.reason,
        mode=mode,
        decider=plan.decider,
    )


def _shadow_decision(plan: Plan, rejections: Mapping[str, str]) -> RouteDecision | None:
    """Return ``plan``'s decision flagged ``shadow``.

    (never served; ``None`` for an empty plan).

    Returns:
        ``plan``'s decision flagged ``shadow``.

    """
    decision = _plan_decision(plan, rejections, mode="user")
    return None if decision is None else replace(decision, shadow=True)


async def _recorded(
    items: AsyncIterator[str | StreamUsage],
    finish: Callable[[tuple[int, int]], None],
    *,
    text_only: bool,
) -> AsyncIterator[str | StreamUsage]:
    """Re-yield ``items`` (text only when ``text_only``).

    Call ``finish(usage)`` once exhausted.

    Yields:
        Each item, text only when ``text_only``.

    """
    usage = (0, 0)
    async for item in items:
        if not isinstance(item, str):
            usage = (int(item.input_tokens), int(item.output_tokens))
            if text_only:
                continue
        yield item
    finish(usage)


@dataclass(frozen=True, kw_only=True)
class RoutedGatewayDependencies:
    """What ``RoutedGateway`` takes, grouped into one value."""

    catalog: ModelCatalog
    """The ``catalog`` value of ``RoutedGateway``."""

    routes: Mapping[str, Route]
    """The ``routes`` value of ``RoutedGateway``."""

    default_route: str
    """The ``default_route`` value of ``RoutedGateway``."""

    policy_store: RoutingPolicyStore
    """The ``policy_store`` value of ``RoutedGateway``."""

    planner: RoutePlanner
    """The ``planner`` value of ``RoutedGateway``."""

    provider_factory: Callable[[LlmConfig], LLMProvider]
    """The ``provider_factory`` value of ``RoutedGateway``."""

    context_provider: RouteContextProvider
    """The ``context_provider`` value of ``RoutedGateway``."""

    executor: ChainExecutor | None = None
    """The ``executor`` value of ``RoutedGateway``."""

    sink: RouteDecisionSink | None = None
    """The ``sink`` value of ``RoutedGateway``."""

    prices: PriceTable | None = None
    """The ``prices`` value of ``RoutedGateway``."""

    shadow_planner: RoutePlanner | None = None
    """The ``shadow_planner`` value of ``RoutedGateway``."""

    classifiers: Sequence[RouteClassifier] = ()
    """The ``classifiers`` value of ``RoutedGateway``."""

    metrics: MetricsProvider | None = None
    """The ``metrics`` value of ``RoutedGateway``."""


@dataclass(frozen=True, kw_only=True)
class RoutedGatewayConfigDependencies:
    """What ``RoutedGateway`` takes, grouped into one value."""

    provider_factory: Callable[[LlmConfig], LLMProvider]
    """The ``provider_factory`` value of ``RoutedGateway``."""

    context_provider: RouteContextProvider
    """The ``context_provider`` value of ``RoutedGateway``."""

    priced_ids: frozenset[str] | None
    """The ``priced_ids`` value of ``RoutedGateway``."""

    embedding: EmbeddingProvider | None = None
    """The ``embedding`` value of ``RoutedGateway``."""

    ledger: TokenLedger | None = None
    """The ``ledger`` value of ``RoutedGateway``."""

    prices: PriceTable | None = None
    """The ``prices`` value of ``RoutedGateway``."""

    facts: FactPublisher | None = None
    """The ``facts`` value of ``RoutedGateway``."""

    metrics: MetricsProvider | None = None
    """The ``metrics`` value of ``RoutedGateway``."""

    planner_deps: Mapping[str, object] | None = None
    """The ``planner_deps`` value of ``RoutedGateway``."""

    policy_store: RoutingPolicyStore | None = None
    """The ``policy_store`` value of ``RoutedGateway``."""


@final
class RoutedGateway(ModelGateway):
    """A gateway that chooses, per request, which catalog endpoint serves."""

    def __init__(self, options: RoutedGatewayDependencies) -> None:
        """Bind the parsed catalog, routes, stages and collaborators."""
        self._catalog = options.catalog
        self._routes = options.routes
        self._default_route = options.default_route
        self._policies = options.policy_store
        self._planner = options.planner
        self._provider_factory = options.provider_factory
        self._context = options.context_provider
        self._executor = options.executor or ChainExecutor(metrics=options.metrics)
        self._sink = options.sink or route_decision_sink_from_config({})
        self._prices = options.prices
        self._classifiers = tuple(options.classifiers)
        self._providers: dict[str, LLMProvider] = {}
        self._sink_failures: MetricCounter | None = None
        if options.metrics is not None:
            self._sink_failures = options.metrics.counter(
                "route_decision_sink_failures_total",
                "Route decisions the sink failed to take (counted, never raised).",
            )
        self._shadow: ShadowRouter | None = None
        if options.shadow_planner is not None:
            self._shadow = ShadowRouter(
                options.shadow_planner, _shadow_decision, self._publish
            )

    @classmethod
    def from_config(
        cls, config: RoutedConfig, options: RoutedGatewayConfigDependencies
    ) -> RoutedGateway:
        """Parse and validate ``config`` and build its stages (no provider is built yet).

        A given ``policy_store`` (e.g. one refreshed from a database) replaces the store
        ``config.policy_store`` describes.

        A callee raises ``AppValueError``: ``INVALID_INPUT`` for an unknown stage kind.

        Returns:
            The gateway.

        Raises:
            AppError: ``INVALID_INPUT`` for a defective catalog, route or policy, or a
                stage missing a collaborator it needs.

        """
        catalog = ModelCatalog.from_mapping(config.catalog, priced_ids=options.priced_ids)
        routes = parse_routes(config.routes, catalog)
        if config.default_route and config.default_route not in routes:
            msg = f"default route {config.default_route!r} is not a configured route"
            raise AppError(ErrorCode.INVALID_INPUT, msg)
        classifiers: list[RouteClassifier] = []

        def planner_for(stages: Mapping[str, Mapping[str, object]]) -> RoutePlanner:
            classifier = route_classifier_from_config(
                stages.get("classifier", config.classifier),
                route_names=frozenset(routes),
                default_route=config.default_route,
                routes=routes,
                embedding=options.embedding,
            )
            classifiers.append(classifier)
            ranker_raw = stages.get("ranker", config.ranker)
            if (
                ranker_raw.get("kind") == CandidateRankerKind.COST_AWARE
                and options.prices is None
            ):
                # The gateway ranks with the one table it boots with; none fails the boot.
                msg = "cost_aware ranker requires a price table"
                raise AppError(ErrorCode.INVALID_INPUT, msg)
            ranker = candidate_ranker_from_config(
                ranker_raw,
                prices=options.prices,
                ledger=options.ledger,
                default_slo=config.default_slo,
            )
            return route_planner_from_config(
                stages.get("planner", config.planner),
                options=RoutePlannerDependencies(
                    classifier=classifier,
                    ranker=ranker,
                    routes=routes,
                    planner_deps=options.planner_deps,
                    metrics=options.metrics,
                ),
            )

        planner = planner_for({})
        shadow = (
            planner_for(cast("Mapping[str, Mapping[str, object]]", config.shadow))
            if config.shadow is not None
            else None
        )
        return cls(
            options=RoutedGatewayDependencies(
                catalog=catalog,
                routes=routes,
                default_route=config.default_route,
                policy_store=options.policy_store
                or routing_policy_store_from_config(config.policy_store),
                planner=planner,
                provider_factory=options.provider_factory,
                context_provider=options.context_provider,
                executor=ChainExecutor(
                    config.max_attempts,
                    failure_threshold=config.breaker_failures,
                    recovery_timeout=config.breaker_recovery_s,
                    metrics=options.metrics,
                ),
                sink=route_decision_sink_from_config(config.sink, facts=options.facts),
                prices=options.prices,
                shadow_planner=shadow,
                classifiers=classifiers,
                metrics=options.metrics,
            )
        )

    def _provider(self, ref: CatalogEndpointRef) -> LLMProvider:
        """Return ``ref``'s provider, building it on first use.

        Returns:
            ``ref``'s provider, building it on first use.

        """
        provider = self._providers.get(ref.key)
        if provider is None:
            provider = self._provider_factory(self._catalog.llm_config(ref))
            self._providers[ref.key] = provider
        return provider

    @property
    def wrapped_provider(self) -> LLMProvider:
        """The catalog's first endpoint's provider.

        (identifies the provider family for spans).
        """
        return self._provider(self._catalog.refs[0])

    def _cost(
        self, model_id: str, input_tokens: int, output_tokens: int
    ) -> Decimal | None:
        """Return the priced cost of the tokens on ``model_id``.

        (``None`` when unpriced).

        Returns:
            The priced cost of the tokens on ``model_id``.

        """
        if self._prices is None:
            return None
        return estimate_cost(
            model_id, self._prices, input_tokens=input_tokens, output_tokens=output_tokens
        )

    async def _decide(
        self, ctx: RouteContext, query: str, req: RouteRequirements
    ) -> tuple[tuple[CatalogEndpointRef, ...], RouteDecision]:
        """Filter and plan ``ctx``'s request.

        Return the endpoints to try and the decision.

        Returns:
            The ordered endpoints and the routing decision.

        Raises:
            AppError: Routing plan left no endpoint.

        """
        policy = self._policies.policy(ctx.tenant_id, ctx.workspace_id)
        filtered = filter_candidates(self._catalog.refs, policy, ctx, req)
        rejections = dict(filtered.rejections)
        route_input = PlanInput(
            query=query, candidates=filtered.kept, requirements=req, policy=policy
        )
        if filtered.direct_model is not None:
            reason = "pinned" if filtered.decider == "admin_pin" else "user_choice"
            plan_ordered, decision = filtered.kept, None
            if plan_ordered:
                first = plan_ordered[0]
                decision = RouteDecision(
                    confidence=1.0,
                    model_id=first.model_id,
                    endpoint=first.endpoint,
                    candidates=tuple(ref.key for ref in plan_ordered),
                    rejections=rejections,
                    reason=reason,
                    mode=policy.mode,
                    decider=filtered.decider,
                )
        else:
            if self._shadow is not None:
                self._shadow.submit(ctx, route_input, rejections)
            plan = await self._planner.plan(ctx, route_input)
            plan_ordered = plan.ordered
            decision = _plan_decision(plan, rejections, policy.mode)
        if decision is None:
            raise AppError(ErrorCode.FORBIDDEN, "routing plan left no endpoint")
        estimate = self._cost(decision.model_id, req.prompt_tokens, req.max_tokens)
        return plan_ordered, replace(decision, estimated_cost=estimate)

    def _publish(self, decision: RouteDecision, ctx: RouteContext) -> None:
        """Hand ``decision`` to the sink; count, never raise, a failure."""
        try:
            self._sink.record(decision, ctx)
        except Exception:
            logger.exception("publish failed")
            if self._sink_failures is not None:
                self._sink_failures.inc()

    def _finalize(
        self,
        ctx: RouteContext,
        decision: RouteDecision,
        attempts: tuple[RouteAttempt, ...],
        served: CatalogEndpointRef,
        usage: tuple[int, int],
    ) -> None:
        """Complete ``decision`` with what was served.

        And publish it everywhere it goes.
        """
        input_tokens, output_tokens = usage
        failover = bool(decision.candidates) and served.key != decision.candidates[0]
        final = replace(
            decision,
            model_id=served.model_id,
            endpoint=served.endpoint,
            attempts=attempts,
            reason="failover" if failover else decision.reason,
            tokens=input_tokens + output_tokens,
            actual_cost=self._cost(served.model_id, input_tokens, output_tokens),
        )
        set_route_decision(final)
        with _TRACER.start_as_current_span("route.decision") as span:
            span.set_attribute("route.name", final.route)
            span.set_attribute("route.confidence", final.confidence)
            span.set_attribute("route.model_id", final.model_id)
            span.set_attribute("route.endpoint", final.endpoint)
            span.set_attribute("route.decider", final.decider)
            span.set_attribute("route.reason", final.reason)
            span.set_attribute("route.attempts", len(final.attempts))
            span.set_attribute("route.candidates", list(final.candidates))
        self._publish(final, ctx)

    async def _plan_call(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, streaming: bool
    ) -> tuple[RouteContext, tuple[CatalogEndpointRef, ...], RouteDecision]:
        """Return the request context, the endpoints to try and the pending decision.

        Returns:
            The request context, the endpoints to try and the pending decision.

        """
        ctx = self._context()
        req = _requirements(messages, config, streaming=streaming)
        ordered, decision = await self._decide(ctx, _query(messages), req)
        return ctx, ordered, decision

    @override
    async def resolve(self, ctx: RouteContext) -> LLMProvider:
        """Return the provider the first endpoint of ``ctx``'s plan uses.

        Returns:
            The provider the first endpoint of ``ctx``'s plan uses.

        """
        ordered, _ = await self._decide(ctx, "", RouteRequirements())
        return self._provider(ordered[0])

    @override
    async def complete(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> LLMResponse:
        """Route, then complete down the planned chain.

        Returns:
            The completion.

        """
        ctx, ordered, decision = await self._plan_call(messages, config, streaming=False)
        response, attempts, served = await self._executor.complete(
            ordered, self._provider, lambda provider: provider.complete(messages, config)
        )
        usage = (response.input_tokens, response.output_tokens)
        self._finalize(ctx, decision, attempts, served, usage)
        return response

    async def _stream(
        self, messages: list[LLMMessage], config: LLMConfig | None, *, text_only: bool
    ) -> AsyncIterator[str | StreamUsage]:
        """Route, open a stream down the chain, and finalize once it is consumed.

        Returns:
            The stream, finalized once consumed.

        """
        ctx, ordered, decision = await self._plan_call(messages, config, streaming=True)
        items, attempts, served = await self._executor.stream(
            ordered,
            self._provider,
            lambda provider: provider.stream_with_usage(messages, config),
        )
        return _recorded(
            items,
            lambda usage: self._finalize(ctx, decision, attempts, served, usage),
            text_only=text_only,
        )

    @override
    async def stream(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str]:
        """Route, then stream text from the planned chain.

        Returns:
            The stream of text deltas.

        """
        return cast(
            "AsyncIterator[str]",
            await self._stream(messages, config, text_only=True),
        )

    @override
    async def stream_with_usage(
        self, messages: list[LLMMessage], config: LLMConfig | None = None
    ) -> AsyncIterator[str | StreamUsage]:
        """Route, then stream with usage from the planned chain.

        Returns:
            The stream of text deltas, ending with the usage when the provider reports it.

        """
        return await self._stream(messages, config, text_only=False)

    @override
    def model_name(self) -> str:
        """Return the current step's default model.

        (policy and planning are not consulted).

        Returns:
            The current step's default model.

        """
        step = self._context().step
        route = self._routes.get(step) or self._routes.get(self._default_route)
        if route is None:
            return self.wrapped_provider.model_name()
        ref = next(r for r in self._catalog.refs if r.model_id == route.default_model)
        return self._provider(ref).model_name()

    @override
    async def __aenter__(self) -> Self:
        """Warm the classifiers (embed their exemplars); providers open lazily as built.

        A callee raises ``AppError``: ``UNAVAILABLE`` when a classifier's exemplars can't
        be embedded.

        Returns:
            This instance.

        """
        for classifier in self._classifiers:
            warm = getattr(classifier, "warm", None)
            if warm is not None:
                await warm()
        return self

    @override
    async def __aexit__(self, *exc: object) -> None:
        """Drain the shadow plans, then close every provider this gateway built."""
        if self._shadow is not None:
            await self._shadow.drain()
        for provider in self._providers.values():
            _ = await provider.__aexit__(None, None, None)
        self._providers.clear()


_INTERFACE_CHECK: type[ModelGateway] = RoutedGateway
"""Type-checker assertion that ``RoutedGateway`` satisfies ``ModelGateway``."""
