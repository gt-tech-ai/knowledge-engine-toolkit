"""Vector-backed RetrievalEngine (local dev): embed the query.

Then similarity-search a VectorStore.

Composes an ``EmbeddingProvider`` (Ollama) + a ``VectorStore`` (Qdrant). Only the request
filters named in ``filter_keys`` (payload fields, e.g. a tenant scope) are pushed to the
store — none by default; every other filter is enforced by the
``FilteringRetrievalEngine`` decorator after the store's top_k. Each hit is mapped into a
``RetrievalResult`` preserving every metadata key, so the decorator's policies see them.
The engine reads one collection: a per-call ``index_id`` naming another index cannot be
honoured and is logged as a warning.

When the store also indexes the text for BM25 (``VectorStore.supports_keyword``, e.g.
OpenSearch), the engine runs the k-NN and the keyword search concurrently and fuses the
two rankings (``FusionConfig``, reciprocal-rank fusion by default); each result's
``score`` is then the fused score. Stores without keyword search (Qdrant, the stub) keep
the k-NN path unchanged.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, final, override

from techai_webutils.clients.retrieval.fusion import FusionConfig, fuse
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
from techai_webutils.core.interfaces.sink import CHUNK_ID_KEY
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.vectors import require_matching_dimension

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import (
        VectorSearchResult,
        VectorStore,
    )

logger = get_logger(__name__)


@final
class VectorRetrievalEngine(NoOpAsyncResource, RetrievalEngine):
    """RetrievalEngine over an EmbeddingProvider + VectorStore.

    (embed → payload-filtered search).
    """

    def __init__(
        self,
        embedder: EmbeddingProvider,
        store: VectorStore,
        collection: str,
        *,
        filter_keys: Sequence[str] = (),
        fusion: FusionConfig | None = None,
    ) -> None:
        """Compose the embedder + store; fail loudly if their vector dimensions disagree.

        ``filter_keys`` names the request filters pushed down to the store as payload
        filters. ``fusion`` fuses the k-NN and keyword rankings when the store supports
        keyword search.

        Args:
            embedder: Embeds the query.
            store: Holds the vectors.
            collection: The store's collection.
            filter_keys: The request fields pushed down as filters.
            fusion: How k-NN and keyword hits fuse; ``None`` searches k-NN only.

        """
        require_matching_dimension(embedder, store, collection)
        self._embedder = embedder
        self._store = store
        self._collection = collection
        self._filter_keys = tuple(filter_keys)
        self._fusion = fusion or FusionConfig()

    @override
    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Embed the query (``input_type="query"``), search with the pushed-down filters.

        Map the hits.

        The engine has one collection, so an ``index_id`` other than ``None`` or the
        collection name cannot be routed; it is logged as a warning (a consumer isolating
        scopes by index must not rely on it here) and the collection is searched.

        Args:
            query: The search query.
            top_k: The most chunks to return.
            filters: Metadata every returned chunk must match; ``None`` matches all.
            index_id: The index to search; ``None`` searches the engine's default.

        Returns:
            The retrieved passages.

        """
        if index_id is not None and index_id != self._collection:
            logger.warning(
                "vector retrieval ignores a per-call index_id; it reads one collection",
                index_id=index_id,
                collection=self._collection,
            )
        request = filters or {}
        pushed = {k: request[k] for k in self._filter_keys if k in request}
        embedding = await self._embedder.embed(query, input_type="query")
        dense_search = self._store.search(
            self._collection, embedding.embedding, top_k=top_k, filters=pushed
        )
        if not self._store.supports_keyword:
            return [_to_retrieval_result(hit) for hit in await dense_search]
        dense, keyword = await asyncio.gather(
            dense_search,
            self._store.keyword_search(self._collection, query, top_k, pushed),
        )
        return self._fused(dense, keyword, top_k)

    def _fused(
        self,
        dense: list[VectorSearchResult],
        keyword: list[VectorSearchResult],
        top_k: int,
    ) -> list[RetrievalResult]:
        """Fuse the k-NN and keyword hits by chunk id.

        Each result carries its fused score.

        Args:
            dense: The k-NN hits.
            keyword: The keyword hits.
            top_k: The most passages to return.

        Returns:
            The retrieved passages.

        """
        hits = {hit.chunk_id: hit for hit in (*keyword, *dense)}
        fused = fuse(
            self._fusion,
            [(h.chunk_id, h.score) for h in dense],
            [(h.chunk_id, h.score) for h in keyword],
        )
        results: list[RetrievalResult] = []
        for chunk_id, score in fused[:top_k]:
            result = _to_retrieval_result(hits[chunk_id])
            result.score = score
            results.append(result)
        return results


def _to_retrieval_result(hit: VectorSearchResult) -> RetrievalResult:
    """Map a VectorSearchResult into a RetrievalResult.

    Preserving the full metadata payload.

    ``hit.metadata`` is a freshly-built, owned dict (``qdrant/store.py`` builds one per
    hit) and ``hit`` is discarded right after this mapping, so the result carries it
    directly rather than copying it. The hit's chunk id is added as the ``chunk_id`` key
    when the payload lacks one, so a citation can name its chunk.

    Args:
        hit: The store's hit.

    Returns:
        The mapped result.

    """
    _ = hit.metadata.setdefault(CHUNK_ID_KEY, hit.chunk_id)
    page_raw = hit.metadata.get("page_number", "")
    page_number = int(page_raw) if page_raw.isdigit() else None
    return RetrievalResult(
        document_id=hit.document_id,
        document_name=hit.metadata.get("document_name", ""),
        chunk_content=hit.content,
        score=hit.score,
        page_number=page_number,
        metadata=hit.metadata,
    )
