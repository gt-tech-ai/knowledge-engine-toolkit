"""Abstention: when retrieval returns nothing rather than weak passages.

``AbstainerKind.score_threshold`` (the default) drops every passage scoring below
``RetrievalConfig.min_score`` (0.5) — today's ``MinScore`` floor, on every engine. The score it
reads is whatever the engine returns: the reranker's when reranking is on, the retriever's
first-stage score otherwise. ``none`` keeps every candidate, for a hybrid engine whose fused RRF
scores (about 0.03) are not on the floor's scale, or when a reranker downstream decides.
"""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.retrieval.filtering import MinScore
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.retrieval import PassagePolicy


class AbstainerKind(StrEnum):
    """How retrieval abstains."""

    SCORE_THRESHOLD = "score_threshold"
    """Drop passages scoring below ``min_score`` (the default; today's behaviour)."""
    NONE = "none"
    """Keep every candidate."""


def abstention_policies(kind: AbstainerKind, min_score: float) -> list[PassagePolicy]:
    """Return the passage policies ``kind`` applies before the consumer's own.

    Raises:
        AppValueError: An unknown kind (``INVALID_INPUT``).

    """
    if kind == AbstainerKind.SCORE_THRESHOLD:
        return [MinScore(min_score)]
    if kind == AbstainerKind.NONE:
        return []
    msg = f"unknown abstainer kind: {kind!r}"
    raise AppValueError(msg)
