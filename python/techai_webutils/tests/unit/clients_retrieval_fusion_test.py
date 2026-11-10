"""Unit tests for rank fusion and shard score merging."""

from __future__ import annotations

from operator import itemgetter
from unittest.mock import MagicMock

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.retrieval.fusion import (
    FusionConfig,
    FusionKind,
    Normalization,
    fuse,
    merge_by_score,
    reciprocal_rank_fusion,
)
from techai_webutils.core.errors import AppValueError

_RESULTS = st.lists(
    st.lists(
        st.tuples(st.sampled_from("abcdefghij"), st.floats(0, 1, allow_nan=False)),
        max_size=6,
    ),
    max_size=4,
)


def test_rrf_scores_match_hand_computed_values() -> None:
    """Test that RRF sums ``1 / (k + rank)`` over the rankings holding each id.

    **Why this test is important:**
      - RRF is the default fusion of every hybrid query; a wrong constant or rank origin
        reorders answers silently.

    **What it tests:**
      - with k=60, ``a`` (rank 1 and 2) scores 1/61 + 1/62, ``b`` (rank 2 and 1) the same,
        and ``c`` (rank 3 once) 1/63; the tie between ``a`` and ``b`` keeps first
        appearance
    """
    fused = reciprocal_rank_fusion([["a", "b", "c"], ["b", "a"]])

    assert [item for item, _ in fused] == ["a", "b", "c"]
    assert fused[0][1] == pytest.approx(1 / 61 + 1 / 62)
    assert fused[1][1] == pytest.approx(1 / 61 + 1 / 62)
    assert fused[2][1] == pytest.approx(1 / 63)


def test_rrf_fusion_kind_and_convex() -> None:
    """Test the configured fusions: RRF by kind, convex min-max and z-score.

    **Why this test is important:**
      - ``convex`` lets a tuned deployment weigh BM25 against k-NN; its weight must
        behave.

    **What it tests:**
      - ``fuse`` with the default config equals ``reciprocal_rank_fusion``
      - convex with weight 0.5, min-max: an id first in both rankings scores 1.0
      - z-score puts each ranking's best at +1 and worst at -1; on a tie (``a`` and ``c``
        at 0.5) the id seen first wins
    """
    dense = [("a", 0.9), ("b", 0.5)]
    keyword = [("a", 12.0), ("c", 3.0)]

    rrf = fuse(FusionConfig(), dense, keyword)
    convex = fuse(FusionConfig(kind=FusionKind.CONVEX), dense, keyword)
    z = fuse(
        FusionConfig(kind=FusionKind.CONVEX, normalization=Normalization.Z_SCORE),
        dense,
        [("c", 2.0), ("d", 1.0)],
    )

    assert rrf == reciprocal_rank_fusion([["a", "b"], ["a", "c"]])
    assert convex[0] == ("a", pytest.approx(1.0))
    assert z[0][0] == "a"


@given(_RESULTS, st.integers(1, 10))
def test_merge_by_score_is_the_global_top_k(
    shards: list[list[tuple[str, float]]], k: int
) -> None:
    """Property: merging shards equals the global top k of their union.

    **Why this test is important:**
      - Shards of one index share a score scale; fusing them by rank would demote a strong
        hit on a crowded shard.

    **What it tests:**
      - for any shard results, the merged scores equal the k largest of each id's best
        score

    Args:
        shards: Each shard's results.
        k: How many to keep.

    """
    best: dict[str, float] = {}
    for results in shards:
        for item, score in results:
            best[item] = max(score, best.get(item, score))

    merged = merge_by_score(shards, k)

    assert [s for _, s in merged] == sorted(best.values(), reverse=True)[:k]
    assert all(best[item] == score for item, score in merged)


@given(
    st.lists(
        st.tuples(st.sampled_from("abcdef"), st.floats(0, 1)), unique_by=itemgetter(0)
    ),
    st.lists(
        st.tuples(st.sampled_from("ghijkl"), st.floats(0, 50)), unique_by=itemgetter(0)
    ),
)
def test_convex_with_weight_one_is_the_dense_ranking(
    dense: list[tuple[str, float]], keyword: list[tuple[str, float]]
) -> None:
    """Property: convex fusion with ``dense_weight=1`` ranks the dense ids in dense order.

    **Why this test is important:**
      - Weight 1 must mean "k-NN only"; any keyword leakage would make the weight
        meaningless.

    **What it tests:**
      - the dense ids come first, ordered as min-max normalizing the dense scores orders
        them

    Args:
        dense: The dense ranking.
        keyword: The keyword ranking.

    """
    dense = sorted(dense, key=lambda p: -p[1])
    fused = fuse(FusionConfig(kind=FusionKind.CONVEX, dense_weight=1.0), dense, keyword)

    head = [item for item, _ in fused[: len(dense)]]
    expected = [item for item, _ in sorted(dense, key=lambda p: -p[1])]
    scores = dict(dense)
    assert [scores[i] for i in head] == [scores[i] for i in expected]


def test_bad_fusion_config_is_refused() -> None:
    """Test that an unknown kind or a weight outside ``[0, 1]`` raises ``INVALID_INPUT``.

    **Why this test is important:**
      - A misconfigured fusion must stop the app at boot, not reorder answers at random.

    **What it tests:**
      - ``dense_weight=1.5`` and an unknown kind raise ``AppValueError``
    """
    bogus = MagicMock(spec=FusionConfig)
    bogus.kind = "bogus"

    with pytest.raises(AppValueError):
        _ = fuse(FusionConfig(kind=FusionKind.CONVEX, dense_weight=1.5), [], [])
    with pytest.raises(AppValueError):
        _ = fuse(bogus, [], [])
