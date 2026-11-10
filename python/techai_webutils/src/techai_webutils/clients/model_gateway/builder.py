"""Config-selected ``ModelGateway`` factory over an injected LLM provider factory.

``GatewayConfig.kind`` selects ``passthrough`` (the default: one provider built from
``config.llm``, delegated unchanged) or ``routed`` (a model catalog, policy filter,
planner and fallback chain; imported lazily). The provider factory is injected (the
consumer passes ``clients.llm.new_llm_from_config`` or its own), so this family never
imports the LLM family and each endpoint's provider is built the way the app builds
providers. An unknown kind raises ``AppValueError``.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.model_gateway.passthrough import PassthroughGateway
from techai_webutils.clients.model_gateway.routed.gateway import (
    RoutedGatewayConfigDependencies,
)
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.types.llm import LlmConfig

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.clients.model_gateway.routed import (
        RoutedGateway as _LazyRoutedGateway,
    )
    from techai_webutils.clients.model_gateway.routed.config import RoutedConfig
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.llm import LLMProvider
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.model_gateway import (
        ModelGateway,
        RouteContextProvider,
    )
    from techai_webutils.core.interfaces.routing import RoutingPolicyStore
    from techai_webutils.core.interfaces.token_ledger import TokenLedger
    from techai_webutils.foundation.tokens.cost import PriceTable


class GatewayKind(StrEnum):
    """The model gateway backends."""

    PASSTHROUGH = "passthrough"
    """One provider, delegated unchanged (the default; today's behaviour)."""
    ROUTED = "routed"
    """Per-request routing over a model catalog (filter → plan → execute)."""


@dataclass(frozen=True, slots=True)
class GatewayConfig:
    """Model gateway configuration."""

    kind: str = GatewayKind.PASSTHROUGH
    """A ``GatewayKind`` value."""
    llm: LlmConfig = field(default_factory=LlmConfig)
    """The provider ``passthrough`` wraps (today's per-step model choice)."""
    routed: RoutedConfig | None = None
    """The ``routed`` kind's configuration."""
    allow_byo_endpoint: bool = False
    """Whether a tenant may register its own endpoint (reserved; off)."""
    allow_byo_key: bool = False
    """Whether a tenant may bring its own provider key (reserved; off)."""


@dataclass(frozen=True, kw_only=True)
class ModelGatewayDependencies:
    """What ``new_model_gateway_from_config`` takes, grouped into one value."""

    provider_factory: Callable[[LlmConfig], LLMProvider]
    """Builds a provider from an ``LlmConfig`` (the app's LLM factory)."""

    context_provider: RouteContextProvider = RouteContext
    """Returns the current request's ``RouteContext`` (``routed``)."""

    priced_ids: frozenset[str] | None = None
    """The model ids the price table prices; ``None`` skips the catalog's price check
    (``routed``).
    """

    embedding: EmbeddingProvider | None = None
    """The embedder the ``embedding`` classifier reads (``routed``; pass the per-request
    memo).
    """

    ledger: TokenLedger | None = None
    """The token ledger the ``cost_aware`` ranker's budget bias reads (``routed``)."""

    prices: PriceTable | None = None
    """The price table the ``cost_aware`` ranker and decision costs use (``routed``)."""

    facts: FactPublisher | None = None
    """The fact publisher the ``analytics`` decision sink publishes through (``routed``).
    """

    metrics: MetricsProvider | None = None
    """Counts routed attempts, decision-sink failures and planner fail-opens
    (``routed``).
    """

    planner_deps: Mapping[str, object] | None = None
    """Collaborators a remote planner kind needs (``overlord``: ``transport``)."""

    policy_store: RoutingPolicyStore | None = None
    """The routing policy store to consult instead of the one ``routed.policy_store``
    describes (``routed``; e.g. a refreshing store).
    """


def new_model_gateway_from_config(
    config: GatewayConfig, options: ModelGatewayDependencies
) -> ModelGateway:
    """Build the ``ModelGateway`` selected by ``config.kind``.

    Args:
        config: The gateway configuration.
        options: The settings and collaborators, grouped.

    Returns:
        The ``ModelGateway`` selected by ``config.kind``.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``routed`` without a ``routed`` config, or with a
            defective catalog, route or policy, or a stage missing a collaborator it
            needs.

    """
    if config.kind == GatewayKind.PASSTHROUGH:
        return PassthroughGateway(options.provider_factory(config.llm))
    if config.kind == GatewayKind.ROUTED:
        if config.routed is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "routed gateway requires a routed config"
            )
        routed_gateway_cls = cast(
            "type[_LazyRoutedGateway]",
            importlib.import_module(
                "techai_webutils.clients.model_gateway.routed"
            ).RoutedGateway,
        )

        return routed_gateway_cls.from_config(
            config.routed,
            options=RoutedGatewayConfigDependencies(
                provider_factory=options.provider_factory,
                context_provider=options.context_provider,
                priced_ids=options.priced_ids,
                embedding=options.embedding,
                ledger=options.ledger,
                prices=options.prices,
                facts=options.facts,
                metrics=options.metrics,
                planner_deps=options.planner_deps,
                policy_store=options.policy_store,
            ),
        )
    msg = f"unknown model gateway kind: {config.kind!r}"
    raise AppValueError(msg)
