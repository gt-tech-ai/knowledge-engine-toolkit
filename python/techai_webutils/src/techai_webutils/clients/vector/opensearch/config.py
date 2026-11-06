"""``OpenSearchConfig``: layout, model stamp, vector mode, compression, rescoring and routing."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class IndexLayout(StrEnum):
    """How a collection's orgs share indices."""

    SHARED = "shared"
    """One index for every org of a collection; documents are routed by org."""
    DEDICATED = "dedicated"
    """One index per org of a collection."""


class VectorMode(StrEnum):
    """Where an index keeps its vectors."""

    IN_MEMORY = "in_memory"
    """A full-precision HNSW graph in the k-NN memory (faiss)."""
    ON_DISK = "on_disk"
    """A quantized copy in memory and the full vectors on disk, rescored at query time."""


@dataclass(frozen=True, slots=True)
class OpenSearchConfig:
    """Settings of the ``opensearch`` vector store (the client itself is injected)."""

    layout: IndexLayout = IndexLayout.SHARED
    """Whether a collection's orgs share one index or each get their own."""
    dedicated_orgs: frozenset[str] = frozenset()
    """Orgs that get their own alias and index even under the ``shared`` layout (large orgs);
    every other org stays in the shared, org-routed index. Moving an org in or out of this set
    is a re-index, not a config flip: its existing chunks stay where they were written."""
    org_field: str = "org_id"
    """The metadata field holding an entry's org; every read, count and delete filters on it."""
    model: str = "default"
    """The embedding model new indices are stamped with (``_meta.model``) and named after."""
    model_version: str = "1"
    """The embedding model version new indices are stamped with and named after."""
    request_timeout_seconds: float = 10.0
    """The timeout of every request the store sends."""
    refresh_on_write: bool = False
    """Refresh after each write so it is searchable at once (tests); off, the index refreshes itself."""
    vector_mode: VectorMode = VectorMode.IN_MEMORY
    """The mode every new index is created in; an ``in_memory`` index expected past
    ``disk_mode_threshold_docs`` is still created ``on_disk``."""
    disk_mode_threshold_docs: int = 0
    """An index expected to hold more documents than this is created ``on_disk``; 0 never does."""
    disk_compression_level: str = "16x"
    """The ``compression_level`` of an ``on_disk`` index. On 20k 768-d vectors, with rescoring,
    recall@10 was 0.9395 at ``32x``, 0.9545 at ``16x``, 0.967 at ``8x`` and 0.998 at ``2x``;
    ``4x`` measured 0.82 and rescoring did not raise it. Very small dimensions (4) fail to
    build at ``8x`` and ``16x``; 8 and up build."""
    oversample_factor: float = 0.0
    """Candidates per result the approximate pass fetches before full-precision rescoring;
    0 keeps the domain's per-level default, which still rescores a quantized index. Under a
    truncation it sizes the full-vector rescore window (``ceil(k * factor)``, at least ``k``),
    so set it there: 5 measured recall@10 0.95 at 256 of 768 dimensions, 3 measured 0.93."""
    truncated_dimension: int = 0
    """Matryoshka truncation: new indices search the vector's first this-many components and
    rescore candidates by the full vector kept in ``_source`` only (no second graph); 0
    searches the full vector. Changing it is a re-index (a new ``model_version``): an index
    keeps its creation shape."""
    shard_field: str = "shard"
    """The metadata field holding an entry's shard (multi-domain stores; absent means shard 0)."""
    shard_domains: tuple[tuple[int, str], ...] = ()
    """``(shard, domain)`` pairs routing each shard to a domain (multi-domain stores)."""
    domain_deadline_seconds: float = 0.0
    """A multi-domain search leaves out a domain that has not answered within this many seconds
    (its share of the caller's latency budget) and counts it; 0 waits for every domain."""
