"""Knowledge-base factory — Kind + Config + ``new_knowledge_base_from_config``.

Only the local vector backend ships today (an embedder → a vector store); a managed Bedrock
Knowledge Base is fed through the separate ``clients/kb_ingestion`` ingestor instead (a different
contract — start/poll a sync job, not this chunk-indexing ``KnowledgeBase``). The embedder and store
are built by the caller (``clients.embedding`` and ``clients.vector`` from this config's fields) and
injected, since a client family never imports a sibling family.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.vector_kb.vector import VectorKnowledgeBase
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.knowledge_base import KnowledgeBase
    from techai_webutils.core.interfaces.vector_store import VectorStore


class KnowledgeBaseKind(StrEnum):
    """Selectable knowledge-base backends (chunk-indexing ``KnowledgeBase`` contract)."""

    VECTOR = "vector"
    """The local Ollama-embedder + Qdrant-store chunk indexer (dev)."""


@dataclass(frozen=True, slots=True)
class KnowledgeBaseConfig:
    """Knowledge-base configuration for the local vector-dev stack (Ollama embedder + Qdrant store)."""

    kind: KnowledgeBaseKind = KnowledgeBaseKind.VECTOR
    """Selects the KnowledgeBase implementation (only ``vector`` ships today)."""
    vector_url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint the chunks are upserted into."""
    collection: str = "documents"
    """Qdrant collection name (shared with the retrieval engine so indexed docs are queryable)."""
    embedding_host: str = "http://localhost:11434"
    """Ollama host serving the embedding model."""
    embedding_model: str = "nomic-embed-text"
    """Embedding model name requested from Ollama."""
    embedding_dimension: int = 768
    """Vector dimensionality of ``embedding_model`` (must match the Qdrant collection)."""
    embed_concurrency: int = 1
    """Sub-batches embedded and upserted at once (1: one at a time, for a single local embedder)."""


def new_knowledge_base_from_config(
    config: KnowledgeBaseConfig,
    *,
    embedder: EmbeddingProvider | None = None,
    store: VectorStore | None = None,
) -> KnowledgeBase:
    """Build the ``KnowledgeBase`` selected by ``config.kind`` over the injected embedder and store.

    Raises:
        AppError: ``INVALID_INPUT`` when the ``vector`` kind is selected without an embedder and a
            store.
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    if config.kind is KnowledgeBaseKind.VECTOR:
        if embedder is None or store is None:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                "the vector knowledge base needs an embedder and a store built by the caller",
            )
        return VectorKnowledgeBase(
            embedder, store, config.collection, embed_concurrency=config.embed_concurrency
        )
    msg = f"unknown knowledge base kind: {config.kind!r}"
    raise AppValueError(msg)
