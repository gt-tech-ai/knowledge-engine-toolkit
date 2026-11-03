"""Env-aware embedding provider factory (foundation/logger ``NewFromConfig`` pattern).

Selects the embedding backend from ``EmbeddingConfig.kind``: Ollama (local dev), Amazon Titan on
Bedrock (``bedrock``; e.g. a semantic cache's query embedder on AWS), or a deterministic in-process
stub (``stub``; no infra). A managed knowledge base that embeds server-side needs no separate provider.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider


class EmbeddingKind(StrEnum):
    """Which embedding provider implementation to build."""

    OLLAMA = "ollama"
    """The local Ollama embedding server (``/api/embed``; dev)."""
    BEDROCK = "bedrock"
    """Amazon Titan text embeddings on Bedrock (bedrock-runtime ``InvokeModel``)."""
    STUB = "stub"
    """Deterministic in-process stub (dev/test/all-stubs; no Ollama)."""


@dataclass(frozen=True, slots=True)
class EmbeddingConfig:
    """Embedding provider configuration (the consumer maps its own config section onto it)."""

    kind: EmbeddingKind = EmbeddingKind.OLLAMA
    """Selects the embedding backend (``ollama``, ``bedrock`` or the in-process ``stub``)."""
    host: str = "http://localhost:11434"
    """Ollama server base URL (``ollama`` kind only)."""
    model: str = "nomic-embed-text"
    """Embedding model: the Ollama model name, or the Bedrock model id (``amazon.titan-embed-text-v2:0``)."""
    dimension: int = 768
    """Vector dimensionality the ``model`` produces (must match the vector store)."""
    timeout_seconds: float = 300.0
    """Per-request timeout for the embedding call (``ollama``: the httpx timeout; ``bedrock``: the
    botocore connect and read timeout — set it low for a hot-path query embedder). httpx defaults to 5s, but a CPU-only Ollama
    embed of a full sub-batch takes tens of seconds (and, under the KB's bounded-concurrency fan-out,
    several such calls queue at the server), so the 5s default aborts every real embed with a
    ReadTimeout. This is a safety upper bound, generous because Ollama is the local-dev vector path."""
    region: str = "us-east-1"
    """AWS region of the bedrock-runtime endpoint (``bedrock`` kind only)."""


def new_embedding_from_config(config: EmbeddingConfig) -> EmbeddingProvider:
    """Build the ``EmbeddingProvider`` selected by ``config.kind``. Unknown kinds fail loudly.

    For ``ollama`` the ``httpx.AsyncClient`` is created here and lives for the returned provider's
    lifetime — i.e. the process, since these clients are wired once at startup (a server) or in a
    short-lived script; the OS reclaims the pool on exit. For ``bedrock`` the bedrock-runtime client
    opens lazily on the first embed (no I/O here) and ``aclose`` on the provider releases it.
    """
    if config.kind is EmbeddingKind.OLLAMA:
        import httpx  # noqa: PLC0415

        from techai_webutils.clients.embedding.ollama import OllamaEmbeddingProvider  # noqa: PLC0415

        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaEmbeddingProvider(
            client, model=config.model, dimension=config.dimension
        )
    if config.kind is EmbeddingKind.BEDROCK:
        from techai_webutils.clients.embedding.bedrock import (  # noqa: PLC0415
            BedrockEmbeddingProvider,
            LazyBedrockRuntime,
        )

        return BedrockEmbeddingProvider(
            LazyBedrockRuntime(
                region=config.region, timeout_seconds=config.timeout_seconds
            ),
            model=config.model,
            dimension=config.dimension,
        )
    if config.kind is EmbeddingKind.STUB:
        # Lazy import (no httpx client to build).
        from techai_webutils.clients.embedding.stub import StubEmbeddingProvider  # noqa: PLC0415

        return StubEmbeddingProvider(model=config.model, dimension=config.dimension)
    msg = f"unknown embedding kind: {config.kind!r}"
    raise AppValueError(msg)
