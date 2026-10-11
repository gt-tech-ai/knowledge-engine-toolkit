"""Env-aware embedding provider factory (foundation/logger ``NewFromConfig`` pattern).

Selects the embedding backend from ``EmbeddingConfig.kind``: Ollama (local dev) or a deterministic
in-process stub (``stub``; no infra). A managed knowledge base that embeds server-side (e.g. Bedrock)
needs no separate provider.
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
    STUB = "stub"
    """Deterministic in-process stub (dev/test/all-stubs; no Ollama)."""


@dataclass(frozen=True, slots=True)
class EmbeddingConfig:
    """Embedding provider configuration (the consumer maps its own config section onto it)."""

    kind: EmbeddingKind = EmbeddingKind.OLLAMA
    """Selects the embedding backend (``ollama`` or the in-process ``stub``)."""
    host: str = "http://localhost:11434"
    """Ollama server base URL."""
    model: str = "nomic-embed-text"
    """Embedding model name requested from Ollama."""
    dimension: int = 768
    """Vector dimensionality the ``model`` produces (must match the vector store)."""
    timeout_seconds: float = 300.0
    """Per-request HTTP timeout for the embedding call. httpx defaults to 5s, but a CPU-only Ollama
    embed of a full sub-batch takes tens of seconds (and, under the KB's bounded-concurrency fan-out,
    several such calls queue at the server), so the 5s default aborts every real embed with a
    ReadTimeout. This is a safety upper bound, generous because Ollama is the local-dev vector path."""


def new_embedding_from_config(config: EmbeddingConfig) -> EmbeddingProvider:
    """Build the ``EmbeddingProvider`` selected by ``config.kind`` (Ollama). Unknown kinds fail loudly.

    The ``httpx.AsyncClient`` is created here and lives for the returned provider's lifetime — i.e. the
    process, since these clients are wired once at startup (a server) or in a short-lived script.
    There is no separate teardown seam; the OS reclaims the pool on exit.
    """
    if config.kind is EmbeddingKind.OLLAMA:
        import httpx  # noqa: PLC0415

        from techai_webutils.clients.embedding.ollama import OllamaEmbeddingProvider  # noqa: PLC0415

        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaEmbeddingProvider(
            client, model=config.model, dimension=config.dimension
        )
    if config.kind is EmbeddingKind.STUB:
        # Lazy import (no httpx client to build).
        from techai_webutils.clients.embedding.stub import StubEmbeddingProvider  # noqa: PLC0415

        return StubEmbeddingProvider(model=config.model, dimension=config.dimension)
    msg = f"unknown embedding kind: {config.kind!r}"
    raise AppValueError(msg)
