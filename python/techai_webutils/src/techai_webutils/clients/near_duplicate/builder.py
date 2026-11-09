"""``near_duplicate_detector_from_config``: the config-selected ``NearDuplicateDetector``.

``noop`` (the default) detects nothing and costs nothing. ``minhash`` computes a MinHash
signature over the text's word shingles and finds candidates through LSH band buckets kept
in the injected ``Cache`` (the ``redis`` Kind across pods), verifying each on the whole
signature.
It is a priced dial: CPU per document (``num_perm`` hash functions over at most
``max_shingles`` shingles) and one cache read per band plus one per candidate.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.near_duplicate.minhash.detector import MinHashDetector
from techai_webutils.clients.near_duplicate.minhash.signature import (
    MinHasher,
    lsh_partition,
)
from techai_webutils.clients.near_duplicate.noop.detector import NoopDetector
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.cache import Cache
    from techai_webutils.core.interfaces.near_duplicate import NearDuplicateDetector


class NearDuplicateKind(StrEnum):
    """Which near-duplicate detector to build."""

    NOOP = "noop"
    """Detection off (the default)."""
    MINHASH = "minhash"
    """MinHash signatures with LSH buckets in a cache."""


@dataclass(frozen=True, slots=True)
class NearDuplicateConfig:
    """Selects the near-duplicate detector and its parameters."""

    kind: NearDuplicateKind = NearDuplicateKind.NOOP
    """The detector to build; ``noop`` by default."""
    threshold: float = 0.8
    """``minhash``: the estimated Jaccard similarity a near-duplicate reaches, in ``(0,
    1)``."""
    num_perm: int = 128
    """``minhash``: signature positions (published practice: 128 to 256)."""
    shingle_size: int = 5
    """``minhash``: words per shingle (published practice: word 5-grams)."""
    max_shingles: int = 10_000
    """``minhash``: the shingles a signature reads at most (the smallest hashes); 0 reads
    all."""
    ttl_seconds: int = 30 * 24 * 3600
    """``minhash``: how long a document stays comparable (its cache entries' TTL)."""
    seed: int = 1
    """``minhash``: derives the hash functions; changing it makes every stored signature
    stale."""


def near_duplicate_detector_from_config(
    config: NearDuplicateConfig, *, cache: Cache | None = None
) -> NearDuplicateDetector:
    """Build the ``NearDuplicateDetector`` ``config.kind`` selects.

    Args:
        config: The detector's config.
        cache: ``minhash``: where the signatures and buckets are kept (required).

    Returns:
        The detector.

    Raises:
        AppValueError: An unknown kind, ``minhash`` without a cache, or a parameter out of
            range (``INVALID_INPUT``).

    """
    if config.kind == NearDuplicateKind.NOOP:
        return NoopDetector()
    if config.kind != NearDuplicateKind.MINHASH:
        msg = f"unknown near-duplicate kind: {config.kind!r}"
        raise AppValueError(msg)
    if cache is None or config.ttl_seconds < 1:
        msg = (
            "the minhash near-duplicate detector needs a cache and a positive ttl_seconds"
        )
        raise AppValueError(msg)
    lsh_partition(config.num_perm, config.threshold)
    return MinHashDetector(
        cache,
        MinHasher(config.num_perm, config.seed, config.max_shingles),
        threshold=config.threshold,
        shingle_size=config.shingle_size,
        ttl_seconds=config.ttl_seconds,
    )
