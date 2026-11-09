"""Config-selected LLM provider factory (foundation/logger ``NewFromConfig`` pattern).

``LlmConfig.kind`` selects a stub, a local Ollama model, AWS Bedrock or any
OpenAI-compatible ``/v1/chat/completions`` server; the model id is the consumer's choice
(there is no built-in default). Unknown kinds fail loudly. The Bedrock client is imported
lazily so the stub path never loads the AWS SDK.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

import httpx

from techai_webutils.clients.llm.openai_compatible import OpenAICompatibleLlmProvider
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.types.llm import LlmConfig, LlmKind

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMProvider


__all__ = ["LlmConfig", "LlmKind", "new_llm_from_config"]
"""``LlmConfig`` and ``LlmKind`` live in ``core.types.llm`` and are re-exported here, the
factory's home."""

_MODEL_KINDS = frozenset({LlmKind.OLLAMA, LlmKind.BEDROCK, LlmKind.OPENAI_COMPATIBLE})
"""The kinds that need ``LlmConfig.model`` (every kind but the stub)."""


def _build_bedrock(config: LlmConfig) -> LLMProvider:
    """Build the Bedrock provider, with a fallback chain when one is configured.

    Returns:
        The Bedrock provider, or a fallback pair when ``fallback_model`` is set.

    """
    # Lazy import: keep aiobotocore off the dev/stub path (loaded only in stage/prod).
    from techai_webutils.clients.llm.bedrock import BedrockLlmProvider  # noqa: PLC0415

    primary = BedrockLlmProvider(
        region=config.region, model=config.model, endpoint=config.endpoint
    )
    if not config.fallback_model:
        return primary
    # Wrap with the throttle/5xx fallback chain; the decorator is only loaded here.
    from techai_webutils.clients.llm.decorators import FallbackLlmProvider  # noqa: PLC0415

    fallback = BedrockLlmProvider(
        region=config.region, model=config.fallback_model, endpoint=config.endpoint
    )
    return FallbackLlmProvider(primary, fallback)


def new_llm_from_config(config: LlmConfig) -> LLMProvider:
    """Build the stub, ollama, bedrock or openai_compatible LLMProvider.

    The ollama, bedrock and openai_compatible kinds require ``config.model``
    (``ValueError`` when empty): there is no built-in default, and a provider with no
    model would only fail on its first request.

    Returns:
        The stub, ollama, bedrock or openai_compatible LLMProvider.

    Raises:
        AppValueError: ``config.kind`` llm kind requires a model id, got empty; unknown
            llm kind: ``config.kind``.

    """
    if config.kind is LlmKind.STUB:
        return StubLlmProvider()
    if config.kind in _MODEL_KINDS and not config.model:
        # A provider with no model id defers failure to first invocation, where it
        # surfaces as an opaque Bedrock validation/AccessDenied error or an Ollama "model
        # is required"/404. Fail loudly at construction instead — the same
        # loud-misconfiguration contract as the unknown-kind guard below.
        msg = f"{config.kind} llm kind requires a model id, got empty"
        raise AppValueError(msg)
    if config.kind is LlmKind.OLLAMA:
        # Lazy import: the provider is only built for the local Ollama path. The model
        # must be an Ollama chat model (e.g. "llama3.2") — config sets it.
        from techai_webutils.clients.llm.ollama import OllamaLlmProvider  # noqa: PLC0415

        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaLlmProvider(client, config.model)
    if config.kind is LlmKind.OPENAI_COMPATIBLE:
        headers = {"Authorization": f"Bearer {config.api_key}"} if config.api_key else {}
        client = httpx.AsyncClient(
            base_url=config.host, timeout=config.timeout_seconds, headers=headers
        )
        return OpenAICompatibleLlmProvider(
            client, config.model, stall_timeout_seconds=config.stall_timeout_seconds
        )
    if config.kind is LlmKind.BEDROCK:
        return _build_bedrock(config)
    msg = f"unknown llm kind: {config.kind!r}"
    raise AppValueError(msg)
