"""Knowledge-base factory — Kind + Config + ``new_knowledge_base_from_config``.

Only the local vector backend ships today (Ollama embedder → Qdrant store); a managed Bedrock
Knowledge Base is fed through the separate ``clients/kb_ingestion`` ingestor instead (a different
contract — start/poll a sync job, not this chunk-indexing ``KnowledgeBase``). Heavy backends are
imported lazily so selecting a different kind never loads httpx/qdrant-client.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.knowledge_base import KnowledgeBase


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


def new_knowledge_base_from_config(config: KnowledgeBaseConfig) -> KnowledgeBase:
    """Build the ``KnowledgeBase`` selected by ``config.kind`` (heavy backends imported lazily)."""
    if config.kind is KnowledgeBaseKind.VECTOR:
        from techai_webutils.clients.vector.composition import build_embedder_and_store  # noqa: PLC0415
        from techai_webutils.clients.vector_kb.vector import VectorKnowledgeBase  # noqa: PLC0415

        embedder, store = build_embedder_and_store(
            embedding_host=config.embedding_host,
            embedding_model=config.embedding_model,
            embedding_dimension=config.embedding_dimension,
            vector_url=config.vector_url,
            collection=config.collection,
        )
        return VectorKnowledgeBase(embedder, store, config.collection)
    msg = f"unknown knowledge base kind: {config.kind!r}"
    raise AppValueError(msg)
