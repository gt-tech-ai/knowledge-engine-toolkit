"""``OpenSearchVectorStore``: vectors and BM25 text in OpenSearch, isolated by org.

Each collection is read through an alias that points at one versioned index
(``<collection>-<model>-v<version>``, stamped with its model in ``_meta``). With the ``shared``
layout every org of a collection shares that index and its documents are routed by org; with
``dedicated`` each org has its own alias and index. The first write to an alias creates its index.
A document's ``_id`` is ``<org>/<entry id>``, so two orgs' equal entry ids never collide.

The store adds the org filter to every search, count, lookup, update and delete itself. The org
comes from a scoped view (``for_org``), from the search filters, or from each entry's metadata; a
call that names no org raises ``INVALID_INPUT``.

Re-indexing to a new model writes to both indices (``set_write_targets``), then moves the alias
(``swap_alias``), keeping the old index to swap back to. ``begin_bulk_ingest`` /
``end_bulk_ingest`` turn refresh and replicas off for a backfill and restore them.

The ``AsyncOpenSearch`` client is injected (hosts, SigV4 auth); ``opensearchpy`` is never imported
at run time here. Every request carries ``request_timeout_seconds``.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, cast, override

from techai_webutils.clients.vector.opensearch.config import IndexLayout, OpenSearchConfig
from techai_webutils.clients.vector.opensearch.mapping import (
    EMBEDDING_FIELD,
    TEXT_FIELD,
    alias_name,
    bulk_error,
    doc_id,
    entry_source,
    hit_result,
    index_body,
    index_name,
    knn_query,
    require,
    source_metadata,
    term_filters,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.vector_store import VectorStore
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from opensearchpy import AsyncOpenSearch

    from techai_webutils.core.interfaces.vector_store import (
        VectorEntry,
        VectorSearchResult,
    )

_UPDATE_SCRIPT = (
    "for (e in params.fields.entrySet()) { ctx._source[e.getKey()] = e.getValue(); }"
)
"""Painless: set each given field on the document."""
_BULK_PREVIOUS = "bulk_ingest_previous"
"""The ``_meta`` key holding an index's settings from before ``begin_bulk_ingest``."""


@dataclass
class _State:
    """What every scoped view of one store shares."""

    aliases: set[str] = field(default_factory=set[str])
    """Aliases known to exist."""
    write_targets: dict[str, tuple[str, ...]] = field(
        default_factory=dict[str, tuple[str, ...]]
    )
    """Alias → the indices a write goes to, when not just the alias's own index."""
    lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    """Serializes first-time index creation in this process."""


class OpenSearchVectorStore(NoOpAsyncResource, VectorStore):
    """A ``VectorStore`` over OpenSearch with org isolation and versioned indices."""

    def __init__(
        self,
        client: AsyncOpenSearch,
        *,
        dimension: int,
        config: OpenSearchConfig | None = None,
        org_id: str | None = None,
        state: _State | None = None,
    ) -> None:
        """Bind the client; ``org_id`` scopes the view (see ``for_org``)."""
        self._client = client
        self._dimension = dimension
        self._config = config or OpenSearchConfig()
        self._org = org_id
        self._state = state or _State()
        self._params: dict[str, object] = {
            "request_timeout": self._config.request_timeout_seconds
        }

    def for_org(self, org_id: str) -> OpenSearchVectorStore:
        """Return a view whose every call is scoped to ``org_id``."""
        return OpenSearchVectorStore(
            self._client,
            dimension=self._dimension,
            config=self._config,
            org_id=require(org_id, "an org"),
            state=self._state,
        )

    @property
    @override
    def dimension(self) -> int:
        """The vector dimension of the indices this store creates."""
        return self._dimension

    @property
    def configured_model(self) -> tuple[str, str]:
        """The ``(model, version)`` the store stamps on the indices it creates."""
        return self._config.model, self._config.model_version

    @override
    async def upsert(self, collection: str, entries: Sequence[VectorEntry]) -> None:
        """Write ``entries`` (by stable id) to every write target of their org's alias."""
        by_org: dict[str, list[VectorEntry]] = {}
        for entry in entries:
            by_org.setdefault(self._entry_org(entry), []).append(entry)
        for org, group in by_org.items():
            targets = await self._write_indices(collection, org)
            body: list[dict[str, object]] = []
            for index in targets:
                for entry in group:
                    body.append({"index": self._action(index, entry.id, org)})
                    body.append(entry_source(entry, self._config.org_field, org))
            await self._bulk(body)

    @override
    async def search(
        self,
        collection: str,
        query_embedding: Sequence[float],
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """k-NN search within one org; ``filters`` (with the org, unless scoped) match exactly."""
        fields = dict(filters or {})
        org = self._org or require(fields.get(self._config.org_field), "an org filter")
        fields[self._config.org_field] = org
        alias = self._alias(collection, org)
        if not await self._alias_exists(alias):
            return []
        response = await self._client.search(
            index=alias,
            body=knn_query(query_embedding, top_k, fields),
            params=self._routed(org),
        )
        hits = cast("dict[str, list[dict[str, object]]]", response["hits"])["hits"]
        return [hit_result(hit) for hit in hits]

    @property
    @override
    def supports_keyword(self) -> bool:
        """OpenSearch indexes the chunk text for BM25 beside the vectors."""
        return True

    @override
    async def keyword_search(
        self,
        collection: str,
        query: str,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
    ) -> list[VectorSearchResult]:
        """BM25 search of the chunk text within one org; ``filters`` match exactly, as in ``search``."""
        fields = dict(filters or {})
        org = self._org or require(fields.get(self._config.org_field), "an org filter")
        fields[self._config.org_field] = org
        alias = self._alias(collection, org)
        if not await self._alias_exists(alias):
            return []
        body = {
            "size": top_k,
            "_source": {"excludes": [EMBEDDING_FIELD]},
            "query": {
                "bool": {
                    "must": [{"match": {TEXT_FIELD: query}}],
                    "filter": term_filters(fields),
                }
            },
        }
        response = await self._client.search(
            index=alias, body=body, params=self._routed(org)
        )
        hits = cast("dict[str, list[dict[str, object]]]", response["hits"])["hits"]
        return [hit_result(hit) for hit in hits]

    @override
    async def delete(self, collection: str, ids: Sequence[str]) -> None:
        """Delete entries by id from every index of the scoped org's alias."""
        org = require(self._org, "an org (use for_org)")
        indices = await self._all_indices(collection, org)
        body: list[dict[str, object]] = [
            {"delete": self._action(i, entry_id, org)}
            for i in indices
            for entry_id in ids
        ]
        if body:
            await self._bulk(body)

    @override
    async def delete_by_document(self, collection: str, document_id: str) -> None:
        """Delete every entry of ``document_id`` in the scoped org."""
        org = require(self._org, "an org (use for_org)")
        indices = await self._all_indices(collection, org)
        if indices:
            await self._client.delete_by_query(
                index=",".join(indices),
                body=self._document_query(org, document_id),
                params=self._routed(org) | {"conflicts": "proceed", "refresh": "true"},
            )

    @override
    async def count_by_document(self, collection: str, document_id: str) -> int:
        """Count the entries of ``document_id`` in the scoped org (0 when the alias is absent)."""
        org = require(self._org, "an org (use for_org)")
        alias = self._alias(collection, org)
        if not await self._alias_exists(alias):
            return 0
        response = await self._client.count(
            index=alias,
            body=self._document_query(org, document_id),
            params=self._routed(org),
        )
        return int(cast("int", response["count"]))

    @override
    async def stored_metadata(
        self, collection: str, ids: Sequence[str]
    ) -> dict[str, dict[str, str]]:
        """Return ``{id: metadata}`` for the ``ids`` the scoped org already stored."""
        org = require(self._org, "an org (use for_org)")
        alias = self._alias(collection, org)
        if not ids or not await self._alias_exists(alias):
            return {}
        response = await self._client.mget(
            index=alias,
            body={"ids": [doc_id(org, i) for i in ids]},
            params=self._routed(org),
        )
        found: dict[str, dict[str, str]] = {}
        for doc in cast("list[dict[str, object]]", response["docs"]):
            source = cast("dict[str, object]", doc.get("_source") or {})
            if doc.get("found") and source.get(self._config.org_field) == org:
                found[str(source.get("entry_id", ""))] = source_metadata(source)
        return found

    async def update_metadata(
        self, collection: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` on every entry of ``document_id`` in the scoped org, without re-embedding."""
        org = require(self._org, "an org (use for_org)")
        if self._config.org_field in fields:
            msg = f"update_metadata may not change {self._config.org_field}"
            raise AppValueError(msg)
        indices = await self._all_indices(collection, org)
        if not indices or not fields:
            return
        body = self._document_query(org, document_id) | {
            "script": {"source": _UPDATE_SCRIPT, "params": {"fields": dict(fields)}}
        }
        await self._client.update_by_query(
            index=",".join(indices),
            body=body,
            params=self._routed(org) | {"conflicts": "proceed", "refresh": "true"},
        )

    async def create_index(self, collection: str, *, expected_docs: int = 0) -> str:
        """Create the configured model's index for the scoped alias; return its name.

        An index expected to hold more than ``disk_mode_threshold_docs`` documents is ``on_disk``.
        An index that already exists is kept as it is.
        """
        cfg = self._config
        index = index_name(
            self._alias(collection, self._org), cfg.model, cfg.model_version
        )
        if not await self._client.indices.exists(index=index, params=self._params):
            on_disk = 0 < cfg.disk_mode_threshold_docs < expected_docs
            body = index_body(
                self._dimension,
                cfg.model,
                cfg.model_version,
                on_disk=on_disk,
                compression_level=cfg.disk_compression_level,
            )
            await self._client.indices.create(index=index, body=body, params=self._params)
        return index

    async def current_index(self, collection: str) -> str | None:
        """Return the index the scoped alias reads from, or ``None`` before the first write."""
        alias = self._alias(collection, self._org)
        if not await self._alias_exists(alias):
            return None
        response = await self._client.indices.get_alias(name=alias, params=self._params)
        return sorted(cast("dict[str, object]", response))[0]

    async def index_model(self, index: str) -> tuple[str, str]:
        """Return the ``(model, version)`` ``index`` was created for; ``("", "")`` if it is absent.

        An index this domain does not hold reads as unstamped rather than an error, so a
        multi-domain store can ask every domain in one round trip.
        """
        response = await self._client.indices.get_mapping(
            index=index, params=self._params | {"ignore_unavailable": "true"}
        )
        if index not in response:
            return "", ""
        meta = self._meta(cast("dict[str, object]", response), index)
        return str(meta.get("model", "")), str(meta.get("version", ""))

    async def swap_alias(self, collection: str, new_index: str) -> None:
        """Point the scoped alias at ``new_index`` alone, in one atomic step; old indices stay."""
        alias = self._alias(collection, self._org)
        actions: list[dict[str, object]] = []
        if await self._alias_exists(alias):
            current = await self._client.indices.get_alias(
                name=alias, params=self._params
            )
            actions.extend(
                {"remove": {"index": old, "alias": alias}}
                for old in cast("dict[str, object]", current)
            )
        actions.append({"add": {"index": new_index, "alias": alias}})
        await self._client.indices.update_aliases(
            body={"actions": actions}, params=self._params
        )
        self._state.aliases.add(alias)

    def set_write_targets(self, collection: str, indices: Sequence[str]) -> None:
        """Send this process's writes for the scoped alias to ``indices``; empty restores the alias."""
        alias = self._alias(collection, self._org)
        if indices:
            self._state.write_targets[alias] = tuple(indices)
        else:
            self._state.write_targets.pop(alias, None)

    async def begin_bulk_ingest(self, index: str) -> None:
        """Turn refresh off and replicas to zero for a backfill, recording the previous settings."""
        response = await self._client.indices.get_settings(
            index=index, params=self._params
        )
        settings = cast("dict[str, dict[str, dict[str, object]]]", response)[index][
            "settings"
        ]
        current = cast("dict[str, object]", settings["index"])
        mapping = await self._client.indices.get_mapping(index=index, params=self._params)
        meta = self._meta(cast("dict[str, object]", mapping), index)
        if _BULK_PREVIOUS not in meta:
            previous = {
                "refresh_interval": str(current.get("refresh_interval", "1s")),
                "number_of_replicas": str(current.get("number_of_replicas", "1")),
            }
            await self._put_meta(index, meta | {_BULK_PREVIOUS: previous})
        await self._put_settings(
            index, {"refresh_interval": "-1", "number_of_replicas": "0"}
        )

    async def end_bulk_ingest(self, index: str) -> None:
        """Restore the settings ``begin_bulk_ingest`` recorded and verify them; a no-op without them."""
        mapping = await self._client.indices.get_mapping(index=index, params=self._params)
        meta = self._meta(cast("dict[str, object]", mapping), index)
        previous = cast("dict[str, str] | None", meta.get(_BULK_PREVIOUS))
        if previous is None:
            return
        await self._put_settings(index, previous)
        response = await self._client.indices.get_settings(
            index=index, params=self._params
        )
        applied = cast("dict[str, dict[str, dict[str, dict[str, object]]]]", response)
        current = applied[index]["settings"]["index"]
        if any(str(current.get(key)) != value for key, value in previous.items()):
            msg = f"bulk-ingest settings of {index} were not restored: {previous}"
            raise AppValueError(msg)
        await self._put_meta(
            index, {k: v for k, v in meta.items() if k != _BULK_PREVIOUS}
        )

    def _entry_org(self, entry: VectorEntry) -> str:
        """Return the org ``entry`` belongs to; it must match the scope when there is one."""
        org = entry.metadata.get(self._config.org_field) or self._org
        org = require(org, f"an org on every entry ({self._config.org_field})")
        if self._org is not None and org != self._org:
            msg = f"entry {entry.id} belongs to another org than this store's scope"
            raise AppValueError(msg)
        return org

    def _layout_of(self, org: str | None) -> IndexLayout:
        """Return ``org``'s layout: the configured one, or ``dedicated`` for a dedicated org."""
        if org is not None and org in self._config.dedicated_orgs:
            return IndexLayout.DEDICATED
        return self._config.layout

    def _alias(self, collection: str, org: str | None) -> str:
        """Return the read alias of ``collection`` for ``org`` under its layout."""
        if self._layout_of(org) is IndexLayout.SHARED:
            return alias_name(collection, None)
        return alias_name(collection, require(org, "an org (dedicated layout)"))

    def _routed(self, org: str) -> dict[str, object]:
        """Return the request params, routed by org when the org is in the shared index."""
        if self._layout_of(org) is IndexLayout.SHARED:
            return self._params | {"routing": org}
        return dict(self._params)

    def _action(self, index: str, entry_id: str, org: str) -> dict[str, object]:
        """Return a bulk action header for one entry: its org-qualified id, routed by org if shared."""
        action: dict[str, object] = {"_index": index, "_id": doc_id(org, entry_id)}
        if self._layout_of(org) is IndexLayout.SHARED:
            action["routing"] = org
        return action

    def _document_query(self, org: str, document_id: str) -> dict[str, object]:
        """Return a query matching ``document_id``'s entries in ``org``."""
        fields = {self._config.org_field: org, "document_id": document_id}
        return {"query": {"bool": {"filter": term_filters(fields)}}}

    async def _alias_exists(self, alias: str) -> bool:
        """Whether ``alias`` exists (remembered once seen)."""
        if alias in self._state.aliases:
            return True
        exists = bool(
            await self._client.indices.exists_alias(name=alias, params=self._params)
        )
        if exists:
            self._state.aliases.add(alias)
        return exists

    async def _write_indices(self, collection: str, org: str) -> tuple[str, ...]:
        """Return the indices a write goes to, creating the alias's first index if needed."""
        alias = self._alias(collection, org)
        if targets := self._state.write_targets.get(alias):
            return targets
        if not await self._alias_exists(alias):
            async with self._state.lock:
                if not await self._alias_exists(alias):
                    scoped = self.for_org(org)
                    index = await scoped.create_index(collection)
                    await scoped.swap_alias(collection, index)
        return (alias,)

    async def _all_indices(self, collection: str, org: str) -> list[str]:
        """Return the alias (if it exists) and every write target: where a delete must land."""
        alias = self._alias(collection, org)
        indices = list(self._state.write_targets.get(alias, ()))
        if await self._alias_exists(alias):
            indices.append(alias)
        return indices

    async def _bulk(self, body: list[dict[str, object]]) -> None:
        """Send a bulk request; raise the coded error of any failed item."""
        params = dict(self._params)
        if self._config.refresh_on_write:
            params["refresh"] = "true"
        response = await self._client.bulk(body=body, params=params)
        if error := bulk_error(cast("dict[str, object]", response)):
            raise error

    async def _put_settings(self, index: str, values: Mapping[str, str]) -> None:
        """Apply index settings."""
        await self._client.indices.put_settings(
            index=index, body={"index": dict(values)}, params=self._params
        )

    async def _put_meta(self, index: str, meta: Mapping[str, object]) -> None:
        """Replace the index mapping's ``_meta``."""
        await self._client.indices.put_mapping(
            index=index, body={"_meta": dict(meta)}, params=self._params
        )

    @staticmethod
    def _meta(mapping: Mapping[str, object], index: str) -> dict[str, object]:
        """Return the ``_meta`` of ``index`` from a get-mapping response."""
        body = cast("dict[str, dict[str, dict[str, object]]]", mapping)[index]["mappings"]
        return dict(cast("dict[str, object]", body.get("_meta", {})))
