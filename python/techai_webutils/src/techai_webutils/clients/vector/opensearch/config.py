"""``OpenSearchConfig``: index layout, model stamp, disk mode and shard routing of the OpenSearch store."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class IndexLayout(StrEnum):
    """How a collection's orgs share indices."""

    SHARED = "shared"
    """One index for every org of a collection; documents are routed by org."""
    DEDICATED = "dedicated"
    """One index per org of a collection."""


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
    disk_mode_threshold_docs: int = 0
    """An index expected to hold more documents than this is created ``on_disk``; 0 never does."""
    disk_compression_level: str = "32x"
    """The ``compression_level`` of an ``on_disk`` index."""
    shard_field: str = "shard"
    """The metadata field holding an entry's shard (multi-domain stores; absent means shard 0)."""
    shard_domains: tuple[tuple[int, str], ...] = ()
    """``(shard, domain)`` pairs routing each shard to a domain (multi-domain stores)."""
