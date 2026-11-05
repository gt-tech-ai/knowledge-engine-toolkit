"""Checks over an embedder and a vector store that are composed together."""

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
