"""Config-selected ``ModelGateway`` factory, fronting ``new_llm_from_config``.

``GatewayConfig.kind`` selects ``passthrough`` (the default: one provider built from
``config.llm``, delegated unchanged) or ``routed`` (a model catalog, policy filter, planner and
fallback chain; imported lazily). The provider factory is injected so each endpoint's provider is
built the way the app builds providers. An unknown kind raises ``AppValueError``.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.llm.builder import LlmConfig, new_llm_from_config
from techai_webutils.clients.model_gateway.passthrough import PassthroughGateway
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.core.interfaces.llm import LLMProvider
    from techai_webutils.core.interfaces.model_gateway import ModelGateway


class GatewayKind(StrEnum):
    """The model gateway backends."""

    PASSTHROUGH = "passthrough"
    """One provider, delegated unchanged (the default; today's behaviour)."""
    ROUTED = "routed"
    """Per-request routing over a model catalog (filter → plan → execute)."""


@dataclass(frozen=True, slots=True)
class RoutedConfig:
    """The ``routed`` kind's configuration (catalog, routes, stages); filled by its backend."""


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
) -> ModelGateway:
    """Build the ``ModelGateway`` selected by ``config.kind``.

    Args:
        config: The gateway configuration.
        provider_factory: Builds a provider from an ``LlmConfig`` (the app's LLM factory).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``routed`` until its backend ships.

    """
    if config.kind == GatewayKind.PASSTHROUGH:
        return PassthroughGateway(provider_factory(config.llm))
    if config.kind == GatewayKind.ROUTED:
        raise AppError(ErrorCode.INVALID_INPUT, "routed gateway not available")
    msg = f"unknown model gateway kind: {config.kind!r}"
    raise AppValueError(msg)
