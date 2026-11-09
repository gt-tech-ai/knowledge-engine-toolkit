"""``MultiDomainVectorStore``.

Spread a collection's shards over several OpenSearch domains.

One domain holds a bounded amount of data. An entry may carry its shard in the
``shard_field`` metadata; one that does not goes to the shard a stable hash of its
document id picks, so a document's chunks stay together and documents spread evenly over
the domains without the caller assigning shards. ``shard_domains`` maps every shard to a
domain; writes go to the shard's domain (adding a shard moves documents, so it is a
re-index, not a config flip). Reads, counts and deletes fan out to every domain, so a
document's entries are found wherever its shards live. Shards of one index score on one
scale, so a search merges the domains' hits by score and keeps the global top k (fusion
runs later, across retrievers, never across shards). With ``deadline_seconds`` set, a
search leaves out a domain that has not answered by then and counts it on
``vector_domain_search_cut_total{domain}``, so one slow domain cannot hold the query. An
entry on an unmapped shard raises ``INVALID_INPUT``.
"""

from __future__ import annotations

import asyncio
import hashlib
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.vector_store import VectorStore
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Mapping, Sequence

    from techai_webutils.clients.vector.opensearch.store import OpenSearchVectorStore
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.vector_store import (
        VectorEntry,
        VectorSearchResult,
    )

logger = get_logger(__name__)

DOMAIN_CUT_METRIC = "vector_domain_search_cut_total"
"""Counter of domain searches left out at their deadline, labelled by ``domain``."""


class MultiDomainVectorStore(NoOpAsyncResource, VectorStore):
    """Routes writes by shard to one of several OpenSearch stores and fans reads out.

    To all.
    """

    def __init__(
        self,
        domains: Mapping[str, OpenSearchVectorStore],
        shard_domains: Mapping[int, str],
        *,
        shard_field: str = "shard",
        deadline_seconds: float = 0.0,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Route shard ``n`` to ``domains[shard_domains[n]]``.

        Every mapped domain must exist.

        ``deadline_seconds`` (0: none) bounds each domain's share of a search; ``metrics``
        counts the domains cut at it.

        Raises:
            AppValueError: A multi-domain store needs at least one domain; shards map to
                unknown domains: ``missing``.

        """
        if not domains:
            msg = "a multi-domain store needs at least one domain"
            raise AppValueError(msg)
        if missing := sorted(set(shard_domains.values()) - set(domains)):
            msg = f"shards map to unknown domains: {missing}"
            raise AppValueError(msg)
        self._domains = dict(domains)
        self._shard_domains = dict(shard_domains)
        self._shard_field = shard_field
        self._deadline = deadline_seconds
        self._metrics = metrics
        self._cut = (
            metrics.counter(
                DOMAIN_CUT_METRIC, "Domain searches left out at their deadline."
            )
            if metrics
            else None
        )

    def for_org(self, org_id: str) -> MultiDomainVectorStore:
        """Return a view whose every domain is scoped to ``org_id``.

        Returns:
            A view whose every domain is scoped to ``org_id``.

        """
        return MultiDomainVectorStore(
            {name: store.for_org(org_id) for name, store in self._domains.items()},
            self._shard_domains,
            shard_field=self._shard_field,
            deadline_seconds=self._deadline,
            metrics=self._metrics,
        )

    def domain(self, name: str) -> OpenSearchVectorStore:
        """Return one domain's store (for per-domain index administration and checks).

        Returns:
            One domain's store (for per-domain index administration and checks).

        """
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

        Every domain's alias follows the same model swap, so any domain holding the alias
        names the live index; ``None`` means no domain has been written yet.

        Returns:
            The index the scoped alias reads from on the first domain that has one.

        """
        found = await asyncio.gather(
            *(s.current_index(collection) for s in self._domains.values())
        )
        return next((index for index in found if index is not None), None)

    async def create_index(self, collection: str, *, expected_docs: int = 0) -> str:
        """Create the configured model's index for the scoped alias on every domain.

        Return it.

        Every domain names it alike (the alias, model and version), so one name addresses
        the index on all of them.

        Returns:
            The configured model's index for the scoped alias on every domain.

        """
        names = await asyncio.gather(
            *(
                s.create_index(collection, expected_docs=expected_docs)
                for s in self._domains.values()
            )
        )
        return names[0]

    async def swap_alias(self, collection: str, new_index: str) -> None:
        """Point the scoped alias at ``new_index`` alone on every domain."""
        await asyncio.gather(
            *(s.swap_alias(collection, new_index) for s in self._domains.values())
        )

    async def index_model(self, index: str) -> tuple[str, str]:
        """Return the ``(model, version)`` of ``index``, read on a domain that holds it.

        Returns:
            The ``(model, version)`` of ``index``, read on a domain that holds it.

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
        """Set ``fields`` on ``document_id``'s entries in every domain.

        Without re-embedding.
        """
        await asyncio.gather(
            *(
                s.update_metadata(collection, document_id, fields)
                for s in self._domains.values()
            )
        )

    async def begin_bulk_ingest(self, index: str) -> None:
        """Put ``index`` into bulk-ingest mode on every domain.

        (each records its own settings).
        """
        await asyncio.gather(
            *(s.begin_bulk_ingest(index) for s in self._domains.values())
        )

    async def end_bulk_ingest(self, index: str) -> None:
        """Restore ``index``'s recorded settings on every domain.

        Even when one domain fails.

        A callee raises ``Exception``: The first domain's failure, once every domain has
        been tried.

        """
        results = await asyncio.gather(
            *(s.end_bulk_ingest(index) for s in self._domains.values()),
            return_exceptions=True,
        )
        if failure := next((r for r in results if isinstance(r, BaseException)), None):
            raise failure

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
        """Search every domain; return the global top ``top_k`` by score.

        (late domains cut).

        Returns:
            The hits, best first.

        """
        return await self._merged(
            lambda store: store.search(collection, query_embedding, top_k, filters), top_k
        )

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
        """BM25-search every domain; return the global top ``top_k`` by score.

        (late domains cut).

        Returns:
            The hits, best first.

        """
        return await self._merged(
            lambda store: store.keyword_search(collection, query, top_k, filters), top_k
        )

    async def _merged(
        self,
        search: Callable[[OpenSearchVectorStore], Awaitable[list[VectorSearchResult]]],
        top_k: int,
    ) -> list[VectorSearchResult]:
        """Run ``search`` on every domain, cut the late ones.

        And keep the top ``top_k`` by score.

        Returns:
            The hits, best first.

        """
        results = await asyncio.gather(
            *(self._bounded(name, search(store)) for name, store in self._domains.items())
        )
        merged = sorted((hit for hits in results for hit in hits), key=lambda h: -h.score)
        return merged[:top_k]

    async def _bounded(
        self, name: str, call: Awaitable[list[VectorSearchResult]]
    ) -> list[VectorSearchResult]:
        """Await one domain's search within the deadline.

        A late domain answers nothing.

        Returns:
            The domain's hits, or an empty list when cut at the deadline.

        """
        if not self._deadline:
            return await call
        try:
            return await asyncio.wait_for(call, self._deadline)
        except TimeoutError:
            logger.warning(
                "domain search cut at its deadline",
                domain=name,
                deadline_seconds=self._deadline,
            )
            if self._cut:
                self._cut.inc(domain=name)
            return []

    @override
    async def delete(self, collection: str, ids: Sequence[str]) -> None:
        """Delete ``ids`` from every domain."""
        await asyncio.gather(*(s.delete(collection, ids) for s in self._domains.values()))

    @override
    async def delete_by_document(
        self, collection: str, document_id: str, keep: Sequence[str] = ()
    ) -> None:
        """Delete ``document_id``'s entries, except the ids in ``keep``.

        From every domain.
        """
        await asyncio.gather(
            *(
                s.delete_by_document(collection, document_id, keep)
                for s in self._domains.values()
            )
        )

    @override
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Sum ``document_id``'s entries over every domain.

        Returns:
            The total entry count.

        """
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
        """Merge every domain's stored metadata for ``ids``.

        Returns:
            The stored metadata by id.

        """
        found = await asyncio.gather(
            *(s.stored_metadata(collection, ids) for s in self._domains.values())
        )
        merged: dict[str, dict[str, str]] = {}
        for part in found:
            merged |= part
        return merged

    def _domain_of(self, entry: VectorEntry) -> str:
        """Return the domain of ``entry``'s shard.

        (a hash of its document id when it has none).

        Returns:
            The domain of ``entry``'s shard.

        Raises:
            AppValueError: Entry ``entry.id`` has a non-integer ``self._shard_field``:
                ``raw``; shard ``shard`` of entry ``entry.id`` maps to no domain.

        """
        raw = entry.metadata.get(self._shard_field)
        if raw is None:
            shards = sorted(self._shard_domains)
            digest = hashlib.sha256(entry.document_id.encode()).digest()
            return self._shard_domains[
                shards[int.from_bytes(digest[:8], "big") % len(shards)]
            ]
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
