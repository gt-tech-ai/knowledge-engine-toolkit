"""Fusing the rankings of several retrievers, and merging the shards of one.

Fusion runs only across retrievers whose scores are not comparable (k-NN and BM25):

- ``rrf``: reciprocal-rank fusion, ``Σ 1 / (k + rank)``, which ignores the scores
  entirely;
- ``convex``: ``w · norm(dense) + (1 - w) · norm(keyword)`` with min-max or z-score
  normalization and a fitted weight ``w`` from config.

Shards of one index score on one scale, so ``merge_by_score`` keeps the global top k of
their union instead of fusing them.
"""

from __future__ import annotations

import heapq
import statistics
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Sequence

RRF_K = 60
"""The RRF constant from Cormack et al. (2009): damps the weight of the very top ranks."""


class FusionKind(StrEnum):
    """How the hybrid engine fuses its k-NN and BM25 rankings."""

    RRF = "rrf"
    """Reciprocal-rank fusion (the default; needs no calibration)."""
    CONVEX = "convex"
    """A weighted sum of normalized scores (needs a fitted weight)."""


class Normalization(StrEnum):
    """How ``convex`` puts both retrievers' scores on one scale."""

    MIN_MAX = "min_max"
    """``(s - min) / (max - min)`` within each ranking."""
    Z_SCORE = "z_score"
    """``(s - mean) / stdev`` within each ranking."""


@dataclass(frozen=True, slots=True)
class FusionConfig:
    """Selects the fusion and its parameters."""

    kind: FusionKind = FusionKind.RRF
    """The fusion to apply."""
    rrf_k: int = RRF_K
    """The RRF constant."""
    normalization: Normalization = Normalization.MIN_MAX
    """The ``convex`` normalization."""
    dense_weight: float = 0.5
    """The ``convex`` weight of the k-NN ranking, in ``[0, 1]``; BM25 gets the rest."""


def reciprocal_rank_fusion(
    rankings: Sequence[Sequence[str]], *, k: int = RRF_K
) -> list[tuple[str, float]]:
    """Fuse ``rankings`` (ids, best first) into ``(id, score)`` pairs, best first.

    Each id scores ``Σ 1 / (k + rank)`` over the rankings that hold it (rank from 1). Ties
    keep the order in which ids first appear, scanning the rankings in order.

    Args:
        rankings: The rankings to fuse.
        k: RRF's damping constant.

    Returns:
        The ``(id, score)`` pairs, best first.

    """
    scores: dict[str, float] = {}
    for ranking in rankings:
        for rank, item in enumerate(ranking, start=1):
            scores[item] = scores.get(item, 0.0) + 1.0 / (k + rank)
    return _best_first(scores)


def fuse(
    config: FusionConfig,
    dense: Sequence[tuple[str, float]],
    keyword: Sequence[tuple[str, float]],
) -> list[tuple[str, float]]:
    """Fuse a k-NN and a BM25 ranking of ``(id, score)`` pairs.

    (best first) as ``config`` selects.

    Args:
        config: The fusion method and weights.
        dense: The k-NN ranking.
        keyword: The BM25 ranking.

    Returns:
        The ``(id, score)`` pairs, best first.

    Raises:
        AppValueError: An unknown kind, or a ``dense_weight`` outside ``[0, 1]``
            (``INVALID_INPUT``).

    """
    if config.kind == FusionKind.RRF:
        return reciprocal_rank_fusion(
            [[i for i, _ in dense], [i for i, _ in keyword]], k=config.rrf_k
        )
    if config.kind == FusionKind.CONVEX:
        if not 0.0 <= config.dense_weight <= 1.0:
            msg = f"fusion dense_weight must be in [0, 1], got {config.dense_weight}"
            raise AppValueError(msg)
        scores: dict[str, float] = {}
        for ranking, weight in (
            (dense, config.dense_weight),
            (keyword, 1.0 - config.dense_weight),
        ):
            for item, value in _normalize(ranking, config.normalization):
                scores[item] = scores.get(item, 0.0) + weight * value
        return _best_first(scores)
    msg = f"unknown fusion kind: {config.kind!r}"
    raise AppValueError(msg)


def merge_by_score(
    shard_results: Sequence[Sequence[tuple[str, float]]], k: int
) -> list[tuple[str, float]]:
    """Return the global top ``k`` of the shards' ``(id, score)`` results, best first.

    The shards of one index score on one scale, so their union is ranked by score as is.
    An id found on several shards keeps its best score; ties keep shard order.

    Args:
        shard_results: Each shard's ranking.
        k: The most pairs to keep.

    Returns:
        The global top ``k`` of the shards' ``(id, score)`` results, best first.

    """
    best: dict[str, float] = {}
    for results in shard_results:
        for item, score in results:
            if item not in best or score > best[item]:
                best[item] = score
    order = {item: n for n, item in enumerate(best)}
    return heapq.nsmallest(k, best.items(), key=lambda pair: (-pair[1], order[pair[0]]))


def _normalize(
    ranking: Sequence[tuple[str, float]], how: Normalization
) -> list[tuple[str, float]]:
    """Put one ranking's scores on a common scale (a constant ranking maps to 1).

    Args:
        ranking: The ranking.
        how: The normalization.

    Returns:
        The rescaled pairs; empty for an empty ranking.

    """
    if not ranking:
        return []
    values = [score for _, score in ranking]
    if how == Normalization.Z_SCORE:
        mean = statistics.fmean(values)
        spread = statistics.pstdev(values)
        return [(i, (s - mean) / spread if spread else 1.0) for i, s in ranking]
    low, high = min(values), max(values)
    return [(i, (s - low) / (high - low) if high > low else 1.0) for i, s in ranking]


def _best_first(scores: dict[str, float]) -> list[tuple[str, float]]:
    """Sort ``(id, score)`` by score descending, ties by insertion order.

    Args:
        scores: Each id's score.

    Returns:
        The ``(id, score)`` pairs, best first.

    """
    order = {item: n for n, item in enumerate(scores)}
    return sorted(scores.items(), key=lambda pair: (-pair[1], order[pair[0]]))
