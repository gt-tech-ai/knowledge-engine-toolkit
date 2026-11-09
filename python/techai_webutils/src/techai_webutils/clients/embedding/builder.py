"""Env-aware embedding provider factory (foundation/logger ``NewFromConfig`` pattern).

Selects the embedding backend from ``EmbeddingConfig.kind``: Ollama (local dev), Amazon
Titan on Bedrock (``bedrock``; e.g. a semantic cache's query embedder on AWS), any
OpenAI-compatible ``/v1/embeddings`` server (``openai_compatible``; vLLM, TEI, LiteLLM),
or a deterministic in-process stub (``stub``; no infra). A managed knowledge base that
embeds server-side needs no separate provider.

``query_prefix`` / ``document_prefix`` (empty by default) wrap the provider in
``PrefixedEmbedding`` for an asymmetric model; with both empty the provider is returned
bare, as before.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, cast

import httpx

from techai_webutils.clients.embedding.ollama import OllamaEmbeddingProvider
from techai_webutils.clients.embedding.openai_compatible import (
    OpenAICompatibleEmbeddingProvider,
)
from techai_webutils.clients.embedding.prefix import PrefixedEmbedding
from techai_webutils.clients.embedding.stub import StubEmbeddingProvider
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.embedding.bedrock import (
        BedrockEmbeddingProvider as _LazyBedrockEmbeddingProvider,
    )
    from techai_webutils.clients.embedding.bedrock import (
        LazyBedrockRuntime as _LazyLazyBedrockRuntime,
    )
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider


class EmbeddingKind(StrEnum):
    """Which embedding provider implementation to build."""

    OLLAMA = "ollama"
    """The local Ollama embedding server (``/api/embed``; dev)."""
    BEDROCK = "bedrock"
    """Amazon Titan text embeddings on Bedrock (bedrock-runtime ``InvokeModel``)."""
    STUB = "stub"
    """Deterministic in-process stub (dev/test/all-stubs; no Ollama)."""
    OPENAI_COMPATIBLE = "openai_compatible"
    """Any OpenAI-compatible ``/v1/embeddings`` server (vLLM, TEI, LiteLLM, OpenAI)."""


@dataclass(frozen=True, slots=True)
class EmbeddingConfig:
    """Embedding provider configuration.

    (the consumer maps its own config section onto it).
    """

    kind: EmbeddingKind = EmbeddingKind.OLLAMA
    """Selects the embedding backend (``ollama``, ``bedrock`` or the in-process
    ``stub``)."""
    host: str = "http://localhost:11434"
    """Server base URL: the Ollama host (``ollama``) or the OpenAI-compatible server
    (``openai_compatible``, without the ``/v1`` path)."""
    model: str = "nomic-embed-text"
    """Embedding model: the Ollama or served model name, or the Bedrock model id
    (``amazon.titan-embed-text-v2:0``)."""
    dimension: int = 768
    """Vector dimensionality the ``model`` produces (must match the vector store)."""
    timeout_seconds: float = 300.0
    """Per-request timeout for the embedding call (``ollama``: the httpx timeout;
    ``bedrock``: the botocore connect and read timeout — set it low for a hot-path query
    embedder). httpx defaults to 5s, but a CPU-only Ollama embed of a full sub-batch takes
    tens of seconds (and, under the KB's bounded-concurrency fan-out, several such calls
    queue at the server), so the 5s default aborts every real embed with a ReadTimeout.
    This is a safety upper bound, generous because Ollama is the local-dev vector path."""
    region: str = "us-east-1"
    """AWS region of the bedrock-runtime endpoint (``bedrock`` kind only)."""
    api_key: str = field(default="", repr=False)
    """Bearer key for the ``openai_compatible`` server (secrets layer only; empty sends
    none)."""
    query_prefix: str = ""
    """Text put before every query, e.g. ``"search_query: "`` for nomic-embed-text or
    ``"query: "`` for E5 (empty: none, today's behaviour). Changing it needs a re-embedded
    corpus."""
    document_prefix: str = ""
    """Text put before every indexed passage, e.g. ``"search_document: "`` (nomic) or
    ``"passage: "`` (E5); empty for symmetric models such as Titan."""


def new_embedding_from_config(config: EmbeddingConfig) -> EmbeddingProvider:
    """Build the ``EmbeddingProvider`` selected by ``config.kind``.

    Unknown kinds fail loudly.

    For ``ollama`` and ``openai_compatible`` the ``httpx.AsyncClient`` is created here and
    lives for the returned provider's lifetime — i.e. the process, since these clients are
    wired once at startup (a server) or in a short-lived script; the OS reclaims the pool
    on exit. For ``bedrock`` the bedrock-runtime client opens lazily on the first embed
    (no I/O here) and ``aclose`` on the provider releases it. A configured prefix wraps
    the provider in ``PrefixedEmbedding``.

    Returns:
        The ``EmbeddingProvider`` selected by ``config.kind``.

    """
    provider = _provider(config)
    if not (config.query_prefix or config.document_prefix):
        return provider
    return PrefixedEmbedding(
        provider,
        query_prefix=config.query_prefix,
        document_prefix=config.document_prefix,
    )


def _provider(config: EmbeddingConfig) -> EmbeddingProvider:
    """Build the bare provider ``config.kind`` selects.

    Returns:
        The bare provider ``config.kind`` selects.

    Raises:
        AppValueError: Unknown embedding kind: ``config.kind``.

    """
    if config.kind is EmbeddingKind.OLLAMA:
        client = httpx.AsyncClient(base_url=config.host, timeout=config.timeout_seconds)
        return OllamaEmbeddingProvider(
            client, model=config.model, dimension=config.dimension
        )
    if config.kind is EmbeddingKind.OPENAI_COMPATIBLE:
        headers = {"Authorization": f"Bearer {config.api_key}"} if config.api_key else {}
        client = httpx.AsyncClient(
            base_url=config.host, timeout=config.timeout_seconds, headers=headers
        )
        return OpenAICompatibleEmbeddingProvider(
            client, model=config.model, dimension=config.dimension
        )
    if config.kind is EmbeddingKind.BEDROCK:
        bedrock_embedding_provider_cls = cast(
            "type[_LazyBedrockEmbeddingProvider]",
            importlib.import_module(
                "techai_webutils.clients.embedding.bedrock"
            ).BedrockEmbeddingProvider,
        )
        lazy_bedrock_runtime_cls = cast(
            "type[_LazyLazyBedrockRuntime]",
            importlib.import_module(
                "techai_webutils.clients.embedding.bedrock"
            ).LazyBedrockRuntime,
        )

        return bedrock_embedding_provider_cls(
            lazy_bedrock_runtime_cls(
                region=config.region, timeout_seconds=config.timeout_seconds
            ),
            model=config.model,
            dimension=config.dimension,
        )
    if config.kind is EmbeddingKind.STUB:
        return StubEmbeddingProvider(model=config.model, dimension=config.dimension)
    msg = f"unknown embedding kind: {config.kind!r}"
    raise AppValueError(msg)
