"""MinHash signatures over word shingles.

And the LSH band split for a similarity threshold.

A document's text becomes the set of its lowercased word ``size``-grams (shingles), each
hashed to 61 bits. Its signature holds, per hash function ``h_i(x) = (a_i * x + b_i) mod
P`` (``P`` the Mersenne prime 2^61 - 1, ``a_i`` and ``b_i`` derived from ``seed`` and
``i`` by BLAKE2b), the minimum over its shingles; two signatures agree at a position with
probability equal to the sets' Jaccard similarity. A very long text keeps only its
``max_shingles``
smallest shingle hashes: the choice depends only on the hash, so two near-copies keep the
same shingles and the estimate stays unbiased, while the cost stays bounded.

LSH splits a signature into ``bands`` of ``rows``; two documents become candidates when
any band matches whole, which happens with probability ``1 - (1 - s^rows)^bands`` at
similarity ``s``. ``lsh_partition`` picks the split whose curve's midpoint,
``(1/bands)^(1/rows)``, is the highest not above the threshold, favouring recall:
verification on the whole signature removes the false candidates.
"""

from __future__ import annotations

import hashlib
import re
import struct
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Iterable, Sequence

MERSENNE_61 = (1 << 61) - 1
"""The prime the hash functions are taken modulo."""

_WORD = re.compile(r"\w+")
"""A word: a run of letters, digits and underscores (Unicode-aware)."""


def word_shingles(text: str, size: int) -> frozenset[int]:
    """Return the 61-bit hashes of ``text``'s lowercased word ``size``-grams.

    A text with fewer than ``size`` words is one shingle of all its words; a text with no
    words has none.

    Args:
        text: The text.
        size: Words per shingle (at least 1).

    Returns:
        The shingle hashes.

    Raises:
        AppValueError: ``size`` is below 1 (``INVALID_INPUT``).

    """
    if size < 1:
        msg = f"shingle size must be at least 1, got {size}"
        raise AppValueError(msg)
    words = _WORD.findall(text.lower())
    if not words:
        return frozenset()
    grams = (
        [" ".join(words)]
        if len(words) < size
        else [" ".join(words[i : i + size]) for i in range(len(words) - size + 1)]
    )
    return frozenset(_hash61(gram.encode()) for gram in grams)


def _hash61(data: bytes) -> int:
    """Return ``data``'s 61-bit hash (BLAKE2b, reduced modulo ``MERSENNE_61``).

    Returns:
        ``data``'s 61-bit hash (BLAKE2b, reduced modulo ``MERSENNE_61``).

    """
    digest = hashlib.blake2b(data, digest_size=8).digest()
    return int.from_bytes(digest, "big") % MERSENNE_61


@dataclass(frozen=True, slots=True)
class MinHasher:
    """Computes ``num_perm``-position MinHash signatures with hash functions derived.

    From ``seed``.
    """

    num_perm: int = 128
    """Positions per signature (hash functions)."""
    seed: int = 1
    """Derives the hash functions; signatures compare only under the same seed and
    ``num_perm``."""
    max_shingles: int = 10_000
    """Shingles a signature is computed over at most (the smallest hashes); 0 keeps
    all."""
    _functions: tuple[tuple[int, int], ...] = field(init=False, repr=False)
    """The ``(a, b)`` of each hash function."""

    def __post_init__(self) -> None:
        """Derive the hash functions; refuse fewer than one position.

        Raises:
            AppValueError: ``num_perm`` is below 1 or ``max_shingles`` is negative
                (``INVALID_INPUT``).

        """
        if self.num_perm < 1 or self.max_shingles < 0:
            msg = (
                f"invalid MinHash shape: {self.num_perm} positions, {self.max_shingles} "
                f"shingles"
            )
            raise AppValueError(msg)
        functions = tuple(
            (
                1 + _hash61(f"{self.seed}:a:{i}".encode()) % (MERSENNE_61 - 1),
                _hash61(f"{self.seed}:b:{i}".encode()),
            )
            for i in range(self.num_perm)
        )
        object.__setattr__(self, "_functions", functions)

    def signature(self, shingles: Iterable[int]) -> tuple[int, ...]:
        """Return the signature of ``shingles``.

        (every position ``MERSENNE_61`` for an empty set).

        Args:
            shingles: The shingle hashes (``word_shingles``).

        Returns: One minimum per hash function.

        Returns:
            The signature of ``shingles``.

        """
        kept = sorted(set(shingles))
        if self.max_shingles:
            kept = kept[: self.max_shingles]
        if not kept:
            return (MERSENNE_61,) * self.num_perm
        return tuple(
            min((a * x + b) % MERSENNE_61 for x in kept) for a, b in self._functions
        )


def similarity(left: Sequence[int], right: Sequence[int]) -> float:
    """Return the fraction of positions where two signatures agree (the Jaccard estimate).

    Args:
        left: A signature.
        right: A signature of the same length.

    Returns:
        The estimate, in ``[0, 1]``.

    Raises:
        AppValueError: The signatures differ in length or are empty (``INVALID_INPUT``).

    """
    if not left or len(left) != len(right):
        msg = f"cannot compare signatures of {len(left)} and {len(right)} positions"
        raise AppValueError(msg)
    return sum(a == b for a, b in zip(left, right, strict=True)) / len(left)


def jaccard(left: frozenset[int], right: frozenset[int]) -> float:
    """Return the exact Jaccard similarity of two shingle sets (1.0 for two empty sets).

    Args:
        left: A shingle set.
        right: A shingle set.

    Returns:
        ``|left & right| / |left | right|``.

    """
    union = left | right
    return len(left & right) / len(union) if union else 1.0


def lsh_partition(num_perm: int, threshold: float) -> tuple[int, int]:
    """Return the ``(bands, rows)`` split of a ``num_perm`` signature for ``threshold``.

    Of the splits with ``bands * rows == num_perm``, the one whose curve midpoint
    ``(1/bands)^(1/rows)`` is the highest not above ``threshold`` (the lowest midpoint
    when every one is above it).

    Args:
        num_perm: Signature positions.
        threshold: The similarity a near-duplicate reaches, in ``(0, 1)``.

    Returns: Bands and rows per band.

    Returns:
        The ``(bands, rows)`` split of a ``num_perm`` signature for ``threshold``.

    Raises:
        AppValueError: ``num_perm`` is below 1 or ``threshold`` is outside ``(0, 1)``
            (``INVALID_INPUT``).

    """
    if num_perm < 1 or not 0.0 < threshold < 1.0:
        msg = f"no LSH split for {num_perm} positions at threshold {threshold}"
        raise AppValueError(msg)
    splits = [(b, num_perm // b) for b in range(1, num_perm + 1) if num_perm % b == 0]
    midpoints = [((1 / b) ** (1 / r), (b, r)) for b, r in splits]
    below = [m for m in midpoints if m[0] <= threshold]
    return max(below)[1] if below else min(midpoints)[1]


def band_keys(signature: Sequence[int], bands: int, rows: int) -> list[str]:
    """Return one hex key per band of ``signature``: band ``i``'s rows, hashed.

    Args:
        signature: The signature (at least ``bands * rows`` positions).
        bands: Bands.
        rows: Positions per band.

    Returns:
        ``bands`` keys; equal keys at the same index mean the band matched whole.

    """
    keys: list[str] = []
    for band in range(bands):
        packed = struct.pack(f">{rows}Q", *signature[band * rows : (band + 1) * rows])
        keys.append(hashlib.blake2b(packed, digest_size=8).hexdigest())
    return keys
