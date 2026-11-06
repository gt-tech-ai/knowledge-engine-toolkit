"""``MultiDomainVectorStore``: spread a collection's shards over several OpenSearch domains.

One domain holds a bounded amount of data. Each entry carries its shard in the
``shard_field`` metadata (absent means shard 0), and ``shard_domains`` maps every shard to a domain;
writes go to the shard's domain. Reads, counts and deletes fan out to every domain, so a document's
entries are found wherever its shards live, and searches return each domain's hits for the caller
to fuse (E7's reciprocal-rank fusion). An entry on an unmapped shard raises ``INVALID_INPUT``.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.vector_store import VectorStore
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.clients.vector.opensearch.store import OpenSearchVectorStore
    from techai_webutils.core.interfaces.vector_store import (
        VectorEntry,
        VectorSearchResult,
    )


class MultiDomainVectorStore(NoOpAsyncResource, VectorStore):
    """Routes writes by shard to one of several OpenSearch stores and fans reads out to all."""

    def __init__(
        self,
        domains: Mapping[str, OpenSearchVectorStore],
        shard_domains: Mapping[int, str],
        *,
        shard_field: str = "shard",
    ) -> None:
        """Route shard ``n`` to ``domains[shard_domains[n]]``; every mapped domain must exist."""
        if not domains:
            msg = "a multi-domain store needs at least one domain"
            raise AppValueError(msg)
        if missing := sorted(set(shard_domains.values()) - set(domains)):
            msg = f"shards map to unknown domains: {missing}"
            raise AppValueError(msg)
        self._domains = dict(domains)
        self._shard_domains = dict(shard_domains)
        self._shard_field = shard_field

    def for_org(self, org_id: str) -> MultiDomainVectorStore:
        """Return a view whose every domain is scoped to ``org_id``."""
        return MultiDomainVectorStore(
            {name: store.for_org(org_id) for name, store in self._domains.items()},
            self._shard_domains,
            shard_field=self._shard_field,
        )

    def domain(self, name: str) -> OpenSearchVectorStore:
        """Return one domain's store (for index administration such as bulk-ingest mode)."""
        return self._domains[name]

    @property
    @override
    def dimension(self) -> int:
        """The vector dimension shared by every domain."""
        return next(iter(self._domains.values())).dimension

    @property
    def configured_model(self) -> tuple[str, str]:
        """The ``(model, version)`` every domain stamps on the indices it creates."""
        return next(iter(self._domains.values())).configured_model

    async def current_index(self, collection: str) -> str | None:
        """Return the index the scoped alias reads from on the first domain that has one.

        Every domain's alias follows the same model swap, so any domain holding the alias names
        the live index; ``None`` means no domain has been written yet.
        """
        found = await asyncio.gather(
            *(s.current_index(collection) for s in self._domains.values())
        )
        return next((index for index in found if index is not None), None)

    async def index_model(self, index: str) -> tuple[str, str]:
        """Return the ``(model, version)`` of ``index``, read on a domain that holds it.

        Raises:
            AppValueError: No domain holds ``index`` (``INVALID_INPUT``).

        """
        stamps = await asyncio.gather(
            *(s.index_model(index) for s in self._domains.values())
        )
        if stamp := next((s for s in stamps if s != ("", "")), None):
            return stamp
        msg = f"no domain holds index {index}"
        raise AppValueError(msg)

    async def update_metadata(
        self, collection: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` on ``document_id``'s entries in every domain, without re-embedding."""
        await asyncio.gather(
            *(
                s.update_metadata(collection, document_id, fields)
                for s in self._domains.values()
            )
        )

    @override
    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Write each entry to its shard's domain."""
        by_domain: dict[str, list[VectorEntry]] = {}
        for entry in entries:
            by_domain.setdefault(self._domain_of(entry), []).append(entry)
        await asyncio.gather(
            *(
                self._domains[name].upsert(collection, group)
                for name, group in by_domain.items()
            )
        )

    @override
    async def search(
        self,
        collection: str,
        query_embedding: Sequence[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """Search every domain; return all their hits, best score first, for the caller to fuse."""
        results = await asyncio.gather(
            *(
                store.search(collection, query_embedding, top_k, filters)
                for store in self._domains.values()
            )
        )
        return sorted((hit for hits in results for hit in hits), key=lambda h: -h.score)

    @property
    @override
    def supports_keyword(self) -> bool:
        """Every domain is an OpenSearch store, so keyword search is available."""
        return True

    @override
    async def keyword_search(
        self,
        collection: str,
        query: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """BM25-search every domain; return all their hits, best score first."""
        results = await asyncio.gather(
            *(
                store.keyword_search(collection, query, top_k, filters)
                for store in self._domains.values()
            )
        )
        return sorted((hit for hits in results for hit in hits), key=lambda h: -h.score)

    @override
    async def delete(self, collection: str, ids: Sequence[str]) -> None:
        """Delete ``ids`` from every domain."""
        await asyncio.gather(*(s.delete(collection, ids) for s in self._domains.values()))

    @override
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete ``document_id``'s entries from every domain."""
        await asyncio.gather(
            *(
                s.delete_by_document(collection, document_id)
                for s in self._domains.values()
            )
        )

    @override
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Sum ``document_id``'s entries over every domain."""
        counts = await asyncio.gather(
            *(
                s.count_by_document(collection, document_id)
                for s in self._domains.values()
            )
        )
        return sum(counts)

    @override
    async def stored_metadata(
        self, collection: str, ids: Sequence[str]
    ) -> dict[str, dict[str, str]]:
        """Merge every domain's stored metadata for ``ids``."""
        found = await asyncio.gather(
            *(s.stored_metadata(collection, ids) for s in self._domains.values())
        )
        merged: dict[str, dict[str, str]] = {}
        for part in found:
            merged |= part
        return merged

    def _domain_of(self, entry: VectorEntry) -> str:
        """Return the domain of ``entry``'s shard."""
        raw = entry.metadata.get(self._shard_field, "0")
        try:
            shard = int(raw)
        except ValueError:
            msg = f"entry {entry.id} has a non-integer {self._shard_field}: {raw!r}"
            raise AppValueError(msg) from None
        domain = self._shard_domains.get(shard)
        if domain is None:
            msg = f"shard {shard} of entry {entry.id} maps to no domain"
            raise AppValueError(msg)
        return domain
