"""Config-selected ``ModelGateway`` factory, fronting ``new_llm_from_config``.

``GatewayConfig.kind`` selects ``passthrough`` (the default: one provider built from
``config.llm``, delegated unchanged) or ``routed`` (a model catalog, policy filter, planner and
fallback chain; imported lazily). The provider factory is injected so each endpoint's provider is
built the way the app builds providers. An unknown kind raises ``AppValueError``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from types import MappingProxyType
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.llm.builder import LlmConfig, new_llm_from_config
from techai_webutils.clients.model_gateway.passthrough import PassthroughGateway
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.model_gateway import RouteContext

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.core.interfaces.llm import LLMProvider
    from techai_webutils.core.interfaces.model_gateway import (
        ModelGateway,
        RouteContextProvider,
    )


class GatewayKind(StrEnum):
    """The model gateway backends."""

    PASSTHROUGH = "passthrough"
    """One provider, delegated unchanged (the default; today's behaviour)."""
    ROUTED = "routed"
    """Per-request routing over a model catalog (filter → plan → execute)."""


def _empty() -> Mapping[str, Any]:
    """Return an empty read-only mapping (a stage or store config left at its defaults)."""
    return MappingProxyType({})


@dataclass(frozen=True, slots=True)
class RoutedConfig:
    """The ``routed`` kind's configuration: catalog, routes and each stage's raw config."""

    catalog: Mapping[str, Any] = field(default_factory=_empty)
    """Model id → capabilities, priors and endpoints (validated at boot)."""
    routes: Mapping[str, Any] = field(default_factory=_empty)
    """Route name → default model, preferred models and classifier exemplars."""
    default_route: str = ""
    """The route a step with no route of its own uses; empty makes that an error."""
    planner: Mapping[str, Any] = field(default_factory=_empty)
    """The planner's config (``kind``: ``local``)."""
    classifier: Mapping[str, Any] = field(default_factory=_empty)
    """The classifier's config (``kind``: ``stub``)."""
    ranker: Mapping[str, Any] = field(default_factory=_empty)
    """The ranker's config (``kind``: ``static_order``)."""
    policy_store: Mapping[str, Any] = field(default_factory=_empty)
    """The policy store's config (``kind``: ``config``; ``policies``, ``workspaces``)."""


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


def new_model_gateway_from_config(
    config: GatewayConfig,
    *,
    provider_factory: Callable[[LlmConfig], LLMProvider] = new_llm_from_config,
    context_provider: RouteContextProvider = RouteContext,
    priced_ids: frozenset[str] | None = None,
) -> ModelGateway:
    """Build the ``ModelGateway`` selected by ``config.kind``.

    Args:
        config: The gateway configuration.
        provider_factory: Builds a provider from an ``LlmConfig`` (the app's LLM factory).
        context_provider: Returns the current request's ``RouteContext`` (``routed``).
        priced_ids: The model ids the price table prices; ``None`` skips the catalog's price
            check (``routed``).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``routed`` without a ``routed`` config, or with a
            defective catalog, route or policy.

    """
    if config.kind == GatewayKind.PASSTHROUGH:
        return PassthroughGateway(provider_factory(config.llm))
    if config.kind == GatewayKind.ROUTED:
        if config.routed is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "routed gateway requires a routed config"
            )
        from techai_webutils.clients.model_gateway.routed import (  # noqa: PLC0415
            RoutedGateway,
        )

        return RoutedGateway.from_config(
            config.routed,
            provider_factory=provider_factory,
            context_provider=context_provider,
            priced_ids=priced_ids,
        )
    msg = f"unknown model gateway kind: {config.kind!r}"
    raise AppValueError(msg)
