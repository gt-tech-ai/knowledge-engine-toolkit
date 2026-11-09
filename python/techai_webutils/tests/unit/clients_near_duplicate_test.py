"""Unit tests for the near-duplicate detectors (MinHash math real; the cache mocked)."""

from __future__ import annotations

import threading
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.clients.near_duplicate import (
    NearDuplicateConfig,
    NearDuplicateKind,
    near_duplicate_detector_from_config,
)
from techai_webutils.clients.near_duplicate.minhash.detector import MinHashDetector
from techai_webutils.clients.near_duplicate.minhash.signature import (
    MinHasher,
    jaccard,
    lsh_partition,
    similarity,
    word_shingles,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.cache import Cache

_WORDS = st.lists(
    st.sampled_from([f"w{i}" for i in range(400)]), min_size=30, max_size=120
)
"""Word lists from a 400-word vocabulary, long enough to shingle."""

_REPORT = " ".join(
    f"Quarterly revenue in region {i} grew by {i + 3} percent while costs held flat."
    for i in range(40)
)
"""A document long enough that a few edited words leave it a near-duplicate."""


def _cache() -> tuple[MagicMock, dict[str, bytes]]:
    """Return a mocked ``Cache`` backed by a dict, and the dict."""
    data: dict[str, bytes] = {}
    cache = MagicMock(spec=Cache)

    def _get(key: str) -> bytes | None:
        return data.get(key)

    def _set(key: str, value: bytes, ttl_seconds: int = 300) -> None:
        assert ttl_seconds == 3600
        data[key] = value

    cache.get = AsyncMock(side_effect=_get)
    cache.set = AsyncMock(side_effect=_set)
    return cache, data


@settings(max_examples=60, deadline=None)
@given(words=_WORDS, edits=st.integers(min_value=0, max_value=20))
def test_signature_agreement_estimates_the_jaccard_of_shingle_sets(
    words: list[str], edits: int
) -> None:
    """Test that the fraction of agreeing signature positions tracks the exact Jaccard.

    **Why this test is important:**
      - Verification trusts the signature estimate in place of the shingle sets, which are
        never stored; an estimator off by more than its sampling error would merge or split
        clusters wrongly.

    **What it tests:**
      - for a word list and a copy with up to 20 words replaced, the 256-position estimate
        is within 0.25 of the exact Jaccard of their word 5-gram sets (well over 5 standard
        deviations), and identical texts estimate exactly 1.0
    """
    edited = list(words)
    for i in range(min(edits, len(edited))):
        edited[(i * 7) % len(edited)] = f"x{i}"
    left = word_shingles(" ".join(words), 5)
    right = word_shingles(" ".join(edited), 5)
    hasher = MinHasher(num_perm=256, max_shingles=0)

    estimate = similarity(hasher.signature(left), hasher.signature(right))

    assert abs(estimate - jaccard(left, right)) <= 0.25
    assert similarity(hasher.signature(left), hasher.signature(left)) == pytest.approx(
        1.0
    )


def test_lsh_partition_takes_the_highest_midpoint_not_above_the_threshold() -> None:
    """Test that the band split favours recall for the configured threshold.

    **Why this test is important:**
      - A split whose S-curve midpoint sits above the threshold misses true near-duplicates
        that verification would have kept; one far below costs extra candidate reads.

    **What it tests:**
      - 128 positions at 0.8 split 16 x 8 (midpoint 0.707), at 0.5 split 32 x 4 (0.42)
      - a threshold outside ``(0, 1)`` or no positions raise ``AppValueError``
    """
    assert lsh_partition(128, 0.8) == (16, 8)
    assert lsh_partition(128, 0.5) == (32, 4)
    for num_perm, threshold in ((128, 1.0), (128, 0.0), (0, 0.8)):
        with pytest.raises(AppValueError):
            lsh_partition(num_perm, threshold)


@pytest.mark.asyncio
async def test_minhash_detector_clusters_near_copies_through_lsh_buckets() -> None:
    """Test that a near-copy joins its representative's cluster and an unrelated text does not.

    **Why this test is important:**
      - The cluster id is what retrieval collapses on and what lets a member reuse its
        representative's entities; the bucket lookup must stay a bounded number of reads.

    **What it tests:**
      - the first document represents its own cluster, with its ``ref`` stored
      - a copy with three words changed joins it: the cluster id, the representative's
        ``ref`` and a similarity at or above the threshold
      - an unrelated document starts its own cluster
      - checking the copy again returns the same cluster with one read and no write
      - a check reads at most one key per band, plus its own entry and each candidate's
    """
    cache, _ = _cache()
    detector = near_duplicate_detector_from_config(
        NearDuplicateConfig(kind=NearDuplicateKind.MINHASH, ttl_seconds=3600),
        cache=cast("Cache", cache),
    )
    copy = _REPORT.replace("region 7", "region seven").replace("grew by 12", "rose 12")

    first = await detector.check("org-1:0", "doc-a", _REPORT, ref="hash-a")
    near = await detector.check("org-1:0", "doc-b", copy, ref="hash-b")
    reads = cache.get.await_count
    other = await detector.check("org-1:0", "doc-c", "An unrelated memo about parking.")
    unrelated_reads = cache.get.await_count - reads
    cache.set.reset_mock()
    again = await detector.check("org-1:0", "doc-b", copy, ref="hash-b")

    assert first is not None
    assert near is not None
    assert other is not None
    assert (first.cluster_id, first.representative_ref, first.is_member) == (
        "doc-a",
        "hash-a",
        False,
    )
    assert (near.cluster_id, near.representative_ref, near.is_member) == (
        "doc-a",
        "hash-a",
        True,
    )
    assert near.similarity >= 0.8
    assert (other.cluster_id, other.is_member) == ("doc-c", False)
    assert again == near
    cache.set.assert_not_awaited()
    assert unrelated_reads <= 1 + 16


@pytest.mark.asyncio
async def test_noop_detects_nothing_and_a_bad_config_is_refused() -> None:
    """Test that the default kind costs nothing and a misconfigured ``minhash`` fails at build.

    **Why this test is important:**
      - Near-duplicate detection is a priced dial: the default must do no work, and a
        ``minhash`` with nowhere to keep buckets must fail at the composition root.

    **What it tests:**
      - the ``noop`` kind returns None
      - ``minhash`` without a cache, with a zero TTL or a threshold of 1, and an unknown kind
        raise ``AppValueError``
    """
    noop = near_duplicate_detector_from_config(NearDuplicateConfig())

    assert await noop.check("org-1:0", "doc-a", _REPORT) is None
    cache = cast("Cache", MagicMock(spec=Cache))
    for config, given_cache in (
        (NearDuplicateConfig(kind=NearDuplicateKind.MINHASH), None),
        (NearDuplicateConfig(kind=NearDuplicateKind.MINHASH, ttl_seconds=0), cache),
        (NearDuplicateConfig(kind=NearDuplicateKind.MINHASH, threshold=1.0), cache),
        (NearDuplicateConfig(kind=cast("NearDuplicateKind", "simhash")), cache),
    ):
        with pytest.raises(AppValueError):
            near_duplicate_detector_from_config(config, cache=given_cache)


@pytest.mark.asyncio
async def test_check_computes_the_signature_off_the_event_loop(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Test that shingling and signing a document run on a worker thread.

    **Why this test is important:**
      - A signature over a large document is pure-Python work taking hundreds of milliseconds;
        on the event loop it stalls every other task in the process, including lease heartbeats
        and health checks.

    **What it tests:**
      - ``check`` runs ``word_shingles`` and ``MinHasher.signature`` away from the thread running
        the loop, and still returns the signature they produce.
    """
    loop_thread = threading.get_ident()
    threads: list[int] = []
    real = MinHasher(num_perm=64)

    def shingles(text: str, size: int) -> frozenset[int]:
        threads.append(threading.get_ident())
        return word_shingles(text, size)

    def signature(values: frozenset[int]) -> tuple[int, ...]:
        threads.append(threading.get_ident())
        return real.signature(values)

    monkeypatch.setattr(
        "techai_webutils.clients.near_duplicate.minhash.detector.word_shingles", shingles
    )
    hasher = MagicMock(spec=MinHasher)
    hasher.num_perm = 64
    hasher.signature.side_effect = signature
    cache, _ = _cache()
    detector = MinHashDetector(
        cast("Cache", cache),
        cast("MinHasher", hasher),
        threshold=0.8,
        shingle_size=5,
        ttl_seconds=3600,
    )

    found = await detector.check("scope", "doc-1", _REPORT)

    assert found.signature == real.signature(word_shingles(_REPORT, 5))
    assert len(threads) == 2
    assert loop_thread not in threads
