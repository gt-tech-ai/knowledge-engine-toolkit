"""Config-selected ``CacheVerifier`` factory.

``CacheVerifierConfig.kind`` selects ``stub`` (confirms nothing, the default) or ``llm``
(asks the injected, already-decorated model provider). The provider is injected, never
built here. An unknown kind, or ``llm`` without a provider, raises ``AppValueError``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.semantic_cache.verifier.llm import (
    CacheVerifierPrompt,
    LlmCacheVerifier,
)
from techai_webutils.clients.semantic_cache.verifier.stub import StubCacheVerifier
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMProvider
    from techai_webutils.core.interfaces.semantic_cache import CacheVerifier


class CacheVerifierKind(StrEnum):
    """The verifier backends."""

    STUB = "stub"
    """Confirms nothing (the default; no model call)."""
    LLM = "llm"
    """Asks the injected model provider."""


@dataclass(frozen=True, slots=True)
class CacheVerifierConfig:
    """Cache verifier configuration."""

    kind: str = CacheVerifierKind.STUB
    """A ``CacheVerifierKind`` value."""


def cache_verifier_from_config(
    config: CacheVerifierConfig,
    *,
    llm: LLMProvider | None = None,
    prompt: CacheVerifierPrompt | None = None,
) -> CacheVerifier:
    """Build the verifier ``config.kind`` selects.

    Args:
        config: The verifier configuration.
        llm: The model provider the ``llm`` kind asks (decorated by the caller).
        prompt: The ``llm`` kind's prompt strategy; the default English prompt when
            ``None``.

    Returns: The verifier.

    Returns:
        The verifier ``config.kind`` selects.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind or ``llm`` without a
            provider.

    """
    if config.kind == CacheVerifierKind.STUB:
        return StubCacheVerifier()
    if config.kind != CacheVerifierKind.LLM:
        msg = f"unknown cache verifier kind: {config.kind!r}"
        raise AppValueError(msg)
    if llm is None:
        msg = "the llm cache verifier requires a model provider"
        raise AppValueError(msg)
    return LlmCacheVerifier(llm, prompt)
