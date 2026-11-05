"""Config-selected LLM provider factory (foundation/logger ``NewFromConfig`` pattern).

``LlmConfig.kind`` selects a stub, a local Ollama model or AWS Bedrock; the model id is the
consumer's choice (there is no built-in default). Unknown kinds fail loudly. The Bedrock client is
imported lazily so the stub path never loads the AWS SDK.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.types.llm import LlmConfig, LlmKind

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMProvider


__all__ = ["LlmConfig", "LlmKind", "new_llm_from_config"]
"""``LlmConfig`` and ``LlmKind`` live in ``core.types.llm`` and are re-exported here, the factory's home."""


def new_llm_from_config(config: LlmConfig) -> LLMProvider:
    """Build the LLMProvider selected by ``config.kind`` (stub, ollama, or bedrock).

    The ollama and bedrock kinds require ``config.model`` (``ValueError`` when empty): there is no
    built-in default, and a provider with no model would only fail on its first request.
    """
    if config.kind is LlmKind.STUB:
        return StubLlmProvider()
    if config.kind in {LlmKind.OLLAMA, LlmKind.BEDROCK} and not config.model:
        # A provider with no model id defers failure to first invocation, where it surfaces as an opaque
        # Bedrock validation/AccessDenied error or an Ollama "model is required"/404. Fail loudly at
        # construction instead — the same loud-misconfiguration contract as the unknown-kind guard below.
        msg = f"{config.kind} llm kind requires a model id, got empty"
        raise AppValueError(msg)
    if config.kind is LlmKind.OLLAMA:
        # Lazy import: httpx client + provider are only built for the local Ollama path. The model must
        # be an Ollama chat model (e.g. "llama3.2") — config sets it.
        import httpx  # noqa: PLC0415

        from techai_webutils.clients.llm.ollama import OllamaLlmProvider  # noqa: PLC0415

        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaLlmProvider(client, config.model)
    if config.kind is LlmKind.BEDROCK:
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
    msg = f"unknown llm kind: {config.kind!r}"
    raise AppValueError(msg)
