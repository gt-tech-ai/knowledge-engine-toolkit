"""Abstention: when retrieval returns nothing rather than weak passages.

``AbstainerKind.score_threshold`` (the default) drops every passage scoring below
``RetrievalConfig.min_score`` (0.5) — today's ``MinScore`` floor, on every engine.
``top_score``
is the calibrated form: it keeps the whole pool when its best passage scores at least
``RetrievalConfig.abstain_threshold`` and returns nothing otherwise, so a pool on any
scale (fused RRF scores, a reranker's) is judged by its best passage, and a weak one skips
the LLM call. The score either reads is whatever the engine returns: the reranker's when
reranking
    is
on, the retriever's first-stage score otherwise. ``none`` keeps every candidate.
"""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.retrieval.filtering import MinScore
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.retrieval import PassagePolicy, RetrievalResult


class AbstainerKind(StrEnum):
    """How retrieval abstains."""

    SCORE_THRESHOLD = "score_threshold"
    """Drop passages scoring below ``min_score`` (the default; today's behaviour)."""
    TOP_SCORE = "top_score"
    """Return nothing when the best passage scores below ``abstain_threshold``; else keep
    all."""
    NONE = "none"
    """Keep every candidate."""


def abstention_policies(kind: AbstainerKind, min_score: float) -> list[PassagePolicy]:
    """Return the per-passage policies ``kind`` applies before the consumer's own.

    ``top_score`` judges the pool, not each passage, so it adds none here
    (``TopScoreAbstainer`` wraps the engine instead).

    Raises:
        AppValueError: An unknown kind (``INVALID_INPUT``).

    """
    if kind == AbstainerKind.SCORE_THRESHOLD:
        return [MinScore(min_score)]
    if kind in {AbstainerKind.NONE, AbstainerKind.TOP_SCORE}:
        return []
    msg = f"unknown abstainer kind: {kind!r}"
    raise AppValueError(msg)


class TopScoreAbstainer(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """Returns nothing when the inner engine's best passage scores below a threshold."""

    def __init__(self, inner: RetrievalEngine, threshold: float) -> None:
        """Wrap ``inner``.

        A pool whose best score is below ``threshold`` is dropped whole.
        """
        self._inner = inner
        self._threshold = threshold

    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine; abstain.

        (empty) unless its best passage clears the bar.
        """
        results = await self._inner.retrieve(
            query, top_k=top_k, filters=filters, index_id=index_id
        )
        if not results or max(r.score for r in results) < self._threshold:
            return []
        return results
