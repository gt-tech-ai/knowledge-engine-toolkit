"""Reranking: reorder any engine's passages by a ``Reranker``'s scores.

``RetrievalConfig.reranking_kind`` selects where reranking happens. ``bedrock_rerank`` is
Bedrock ``Retrieve``'s own reranker, so it exists only on the Bedrock engine;
``cross_encoder`` wraps any engine in ``RerankingRetrievalEngine``, which scores the
engine's top_k against the query and returns them best first, each carrying its reranker
score (the abstainer then reads that score).
"""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.reranker import Reranker
    from techai_webutils.core.interfaces.retrieval import RetrievalResult


class RerankingKind(StrEnum):
    """Which reranker reorders the retrieved passages.

    (``RetrievalConfig.reranking_kind``).
    """

    NONE = "none"
    """No reranking (the default)."""
    BEDROCK_RERANK = "bedrock_rerank"
    """Bedrock ``Retrieve``'s ``rerankingConfiguration`` (the ``bedrock`` engine only)."""
    CROSS_ENCODER = "cross_encoder"
    """The injected ``Reranker`` reorders any engine's passages."""


@final
class RerankingRetrievalEngine(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """Scores the inner engine's passages with a ``Reranker``.

    And returns them best first.
    """

    def __init__(self, inner: RetrievalEngine, reranker: Reranker) -> None:
        """Wrap ``inner``; ``reranker`` scores its passages.

        (the composition root owns it).

        Args:
            inner: The engine whose passages are post-processed.
            reranker: Rescores the passages.

        """
        super().__init__(inner)
        self._reranker = reranker

    @override
    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine, then sort by reranker score.

        Each result keeps its score.

        Args:
            query: The search query.
            top_k: The most chunks to return.
            filters: Metadata every returned chunk must match; ``None`` matches all.
            index_id: The index to search; ``None`` searches the engine's default.

        Returns:
            The retrieved passages.

        Raises:
            InternalError: The reranker returned a different number of scores than
                passages.

        """
        results = await self._inner.retrieve(
            query, top_k=top_k, filters=filters, index_id=index_id
        )
        if not results:
            return results
        scores = await self._reranker.rerank(query, [r.chunk_content for r in results])
        if len(scores) != len(results):
            msg = f"reranker returned {len(scores)} scores for {len(results)} passages"
            raise InternalError(msg)
        for result, score in zip(results, scores, strict=True):
            result.score = score
        return sorted(results, key=lambda r: r.score, reverse=True)
