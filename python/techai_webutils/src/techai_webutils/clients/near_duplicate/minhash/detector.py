"""``MinHashDetector``.

Near-duplicate clusters from MinHash signatures and LSH buckets in a cache.

Each checked document leaves two kinds of entries in the injected ``Cache`` (the ``redis``
Kind in a cluster), every one expiring after ``ttl_seconds``:

- ``nd:{scope}:d:{document}`` — its cluster, its ``ref`` and its signature;
- ``nd:{scope}:b:{band}:{key}`` — per LSH band, the first document whose band hashed to
  ``key``.

A check reads the document's own entry first (a retry returns the cluster it got), then
its band buckets; each candidate the buckets name is verified on the whole signature (the
fraction of agreeing positions, the Jaccard estimate) against ``threshold``. The most
similar verified candidate's cluster is the document's; with none it starts its own. It
then claims the buckets no one held, so the bucket keeps pointing at the first document
seen. The lookup is a bounded number of key reads per document (one per band plus the
candidates), never a scan of recent signatures, and memory is bounded by the TTL and the
cache's eviction policy. Two concurrent near-copies may both start a cluster; the cache
has no compare-and-set, and a missed pairing costs only a missed collapse.
"""

from __future__ import annotations

import asyncio
import base64
import json
import struct
from typing import TYPE_CHECKING, override

from techai_webutils.clients.near_duplicate.minhash.signature import (
    MinHasher,
    band_keys,
    lsh_partition,
    similarity,
    word_shingles,
)
from techai_webutils.core.interfaces.near_duplicate import (
    NearDuplicate,
    NearDuplicateDetector,
)

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.cache import Cache


class MinHashDetector(NearDuplicateDetector):
    """Clusters near-duplicates by MinHash with LSH buckets held in a cache."""

    def __init__(
        self,
        cache: Cache,
        hasher: MinHasher,
        *,
        threshold: float,
        shingle_size: int,
        ttl_seconds: int,
    ) -> None:
        """Hold buckets in ``cache``; documents at or above ``threshold`` share a cluster.

        Args:
            cache: Where the documents and buckets are kept.
            hasher: Computes the signatures.
            threshold: The estimated Jaccard similarity a near-duplicate reaches, in ``(0,
                1)``.
            shingle_size: Words per shingle.
            ttl_seconds: How long a document's entries stay comparable.

        """
        self._cache = cache
        self._hasher = hasher
        self._threshold = threshold
        self._shingle_size = shingle_size
        self._ttl = ttl_seconds
        self._bands, self._rows = lsh_partition(hasher.num_perm, threshold)

    def _signature(self, text: str) -> tuple[int, ...]:
        """Return ``text``'s signature; pure-Python CPU work.

        So ``check`` runs it on a thread.
        """
        return self._hasher.signature(word_shingles(text, self._shingle_size))

    @override
    async def check(
        self, scope: str, document_id: str, text: str, *, ref: str = ""
    ) -> NearDuplicate:
        """Return ``document_id``'s cluster within ``scope`` and record it.

        Args:
            scope: The partition documents are compared within.
            document_id: The document.
            text: The document's text.
            ref: Stored with the document, returned to the members of its cluster.

        Returns:
            The cluster.

        """
        own = await self._entry(scope, document_id)
        if own is not None:
            return own
        signature = await asyncio.to_thread(self._signature, text)
        keys = band_keys(signature, self._bands, self._rows)
        holders = [
            await self._cache.get(self._bucket(scope, i, k)) for i, k in enumerate(keys)
        ]
        best = await self._best(scope, document_id, signature, holders)
        found = best or NearDuplicate(
            document_id, document_id, representative_ref=ref, signature=signature
        )
        await self._record(scope, found, ref)
        for i, (key, holder) in enumerate(zip(keys, holders, strict=True)):
            if holder is None:
                await self._cache.set(
                    self._bucket(scope, i, key), document_id.encode(), self._ttl
                )
        return found

    async def _best(
        self,
        scope: str,
        document_id: str,
        signature: tuple[int, ...],
        holders: Sequence[bytes | None],
    ) -> NearDuplicate | None:
        """Return the most similar verified candidate's cluster for ``document_id``.

        If any.
        """
        best: NearDuplicate | None = None
        for candidate in dict.fromkeys(h.decode() for h in holders if h is not None):
            entry = await self._entry(scope, candidate)
            if entry is None or candidate == document_id:
                continue
            score = similarity(signature, entry.signature)
            if score >= self._threshold and (best is None or score > best.similarity):
                best = NearDuplicate(
                    document_id,
                    entry.cluster_id,
                    representative_ref=entry.representative_ref,
                    similarity=score,
                    signature=signature,
                )
        return best

    async def _entry(self, scope: str, document_id: str) -> NearDuplicate | None:
        """Return the recorded cluster of ``document_id``.

        Or None when it was never recorded.
        """
        raw = await self._cache.get(self._document(scope, document_id))
        if raw is None:
            return None
        stored = json.loads(raw)
        packed = base64.b64decode(stored["s"])
        signature = struct.unpack(f">{len(packed) // 8}Q", packed)
        return NearDuplicate(
            document_id,
            stored["c"],
            representative_ref=stored["r"],
            similarity=stored["m"],
            signature=signature,
        )

    async def _record(self, scope: str, found: NearDuplicate, ref: str) -> None:
        """Store ``found`` under its document; a representative keeps its own ``ref``."""
        packed = struct.pack(f">{len(found.signature)}Q", *found.signature)
        stored = {
            "c": found.cluster_id,
            "r": found.representative_ref if found.is_member else ref,
            "m": found.similarity,
            "s": base64.b64encode(packed).decode(),
        }
        await self._cache.set(
            self._document(scope, found.document_id),
            json.dumps(stored).encode(),
            self._ttl,
        )

    @staticmethod
    def _document(scope: str, document_id: str) -> str:
        """Return the key of ``document_id``'s entry."""
        return f"nd:{scope}:d:{document_id}"

    @staticmethod
    def _bucket(scope: str, band: int, key: str) -> str:
        """Return the key of band ``band``'s bucket ``key``."""
        return f"nd:{scope}:b:{band}:{key}"
