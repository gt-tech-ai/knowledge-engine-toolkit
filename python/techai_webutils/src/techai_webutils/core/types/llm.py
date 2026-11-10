"""LLM provider selection.

The ``Kind`` and ``Config`` an LLM factory builds a provider from.

Plain data in core, so every family that names a provider's configuration (the LLM
factory, a model gateway's catalog) shares one definition without importing another client
family.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum


class LlmKind(StrEnum):
    """Which LLM provider implementation to build."""

    STUB = "stub"
    """The deterministic echo provider (dev/local; no Bedrock)."""
    OLLAMA = "ollama"
    """The local Ollama generator (dev/local; real answers via the same Ollama that serves
    embeddings)."""
    BEDROCK = "bedrock"
    """The real AWS Bedrock (Anthropic Claude) provider (stage/prod)."""
    OPENAI_COMPATIBLE = "openai_compatible"
    """Any OpenAI-compatible ``/v1/chat/completions`` server (vLLM, LiteLLM, llama.cpp,
    OpenAI)."""


@dataclass(frozen=True, slots=True)
class LlmConfig:
    """LLM client configuration."""

    kind: LlmKind = LlmKind.STUB
    """Selects the provider implementation."""
    region: str = "us-east-1"
    """AWS region for the Bedrock runtime client (unused by the stub)."""
    endpoint: str | None = None
    """Optional Bedrock endpoint override; ``None`` uses AWS default resolution."""
    model: str = ""
    """Model id to invoke (a Bedrock model or inference-profile id, an Ollama or a served
    model); required for the bedrock, ollama and openai_compatible kinds."""
    fallback_model: str = ""
    """Fallback Bedrock model id; when set on a bedrock provider, throttle/5xx retries
    here. Empty disables the fallback (a bare provider is returned)."""
    host: str = "http://localhost:11434"
    """Server base URL: the Ollama host (``ollama``) or the OpenAI-compatible server
    (``openai_compatible``, without the ``/v1`` path); unused by stub/bedrock."""
    timeout_seconds: float = 300.0
    """Per-request HTTP timeout for the ``ollama`` and ``openai_compatible`` kinds — CPU
    generation is slow and httpx defaults to 5s."""
    api_key: str = field(default="", repr=False)
    """Bearer key for the ``openai_compatible`` server (secrets layer only; empty sends
    none)."""
    stall_timeout_seconds: float = 60.0
    """Longest silence an ``openai_compatible`` stream may keep before it fails with
    ``TIMEOUT``."""
