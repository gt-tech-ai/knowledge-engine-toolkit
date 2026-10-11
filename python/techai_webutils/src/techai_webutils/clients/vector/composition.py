"""Shared wiring for the vector-dev stack (embedder + store) — used by the engine and KB.

Both the ``VectorRetrievalEngine`` and the ``VectorKnowledgeBase`` compose the same Ollama embedder +
Qdrant store pair and enforce the same dimension-agreement invariant. These helpers keep that single
seam in one place so the two consumers cannot drift. ``build_embedder_and_store`` lazy-imports the
heavy backends (httpx / qdrant-client) so importing this module never loads them.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import VectorStore


def require_matching_dimension(
    embedder: EmbeddingProvider, store: VectorStore, collection: str
) -> None:
    """Raise ``ValueError`` if the embedder and store vector dimensions disagree.

    The dimension is the single source of truth for the pair: a mismatch means the query vector and
    the stored vectors have different lengths, so cosine search silently returns nothing. Catch it at
    wiring time rather than as an empty (and inexplicable) result set at query time.
    """
    if embedder.dimension() != store.dimension:
        msg = (
            f"embedding dimension {embedder.dimension()} != vector store dimension "
            f"{store.dimension} (collection {collection!r})"
        )
        raise AppValueError(msg)


def build_embedder_and_store(
    *,
    embedding_host: str,
    embedding_model: str,
    embedding_dimension: int,
    vector_url: str,
    collection: str,
) -> tuple[EmbeddingProvider, VectorStore]:
    """Build the Ollama embedder + Qdrant store pair the vector engine and KB factories share.

    The embedding dimension is threaded into both so they agree by construction (the caller still
    passes the pair through ``require_matching_dimension`` for defence in depth). Heavy backends are
    imported lazily so the stub/bedrock paths that never call this never load httpx / qdrant-client.
    """
    from techai_webutils.clients.embedding import (  # noqa: PLC0415
        EmbeddingConfig,
        EmbeddingKind,
        new_embedding_from_config,
    )
    from techai_webutils.clients.vector import (  # noqa: PLC0415
        VectorStoreConfig,
        VectorStoreKind,
        new_vector_store_from_config,
    )

    embedder = new_embedding_from_config(
        EmbeddingConfig(
            kind=EmbeddingKind.OLLAMA,
            host=embedding_host,
            model=embedding_model,
            dimension=embedding_dimension,
        ),
    )
    store = new_vector_store_from_config(
        VectorStoreConfig(
            kind=VectorStoreKind.QDRANT,
            url=vector_url,
            collection=collection,
            dimension=embedding_dimension,
        ),
    )
    return embedder, store
