"""``BedrockSink``.

Write a document's chunks as files a Bedrock Knowledge Base data source ingests.

Bedrock embeds by itself, so the sink takes no vectors: it writes text files and their
``.metadata.json`` sidecars into the data source's bucket (``BedrockSinkConfig.layout``).
Writes are idempotent: the document's objects are listed once, keys already present are
    skipped
(a redelivery writes nothing), and objects of another version are deleted after the new
ones are written. The caller starts the data source's ingestion job (``StartBudget``)
afterwards; until then Bedrock serves the previous files.
"""

from __future__ import annotations

import asyncio
import json
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.clients.sink.bedrock.layout import (
    SIDECAR_SUFFIX,
    document_prefix,
    sidecar,
    unit_key,
    units,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sink import Sink
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Awaitable, Iterable, Mapping, Sequence

    from techai_webutils.clients.sink.bedrock.config import BedrockSinkConfig
    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument
    from techai_webutils.core.interfaces.storage import StorageClient

_TEXT = "text/plain; charset=utf-8"
"""Content type of a unit file."""

_JSON = "application/json"
"""Content type of a sidecar."""


@final
class BedrockSink(NoOpAsyncResource, Sink):
    """A ``Sink`` writing files and sidecars for a Bedrock data source into one bucket."""

    def __init__(
        self, storage: StorageClient, config: BedrockSinkConfig, *, org_field: str
    ) -> None:
        """Write through ``storage`` as ``config`` says.

        Files carry their org as ``org_field``.

        Args:
            storage: Writes the units and sidecars.
            config: The bucket, prefix and layout.
            org_field: The attribute that carries the organization.

        """
        self._storage = storage
        self._config = config
        self._org_field = org_field
        self._limit = asyncio.Semaphore(config.concurrency)

    @override
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Write the document's files and sidecars that are missing.

        Then delete other versions.

        Args:
            document: The document the chunks belong to.
            chunks: The chunks, in document order.
            vectors: One embedding per chunk, in order; ``None`` for a sink that does not
                store vectors.

        Raises:
            AppValueError: Vectors were passed (Bedrock embeds by itself), or the org or
                document id cannot be a key segment (``INVALID_INPUT``).

        """
        if vectors is not None:
            msg = (
                f"the bedrock sink embeds by itself; got vectors for "
                f"{document.document_id}"
            )
            raise AppValueError(msg)
        doc_prefix = document_prefix(
            self._config.prefix, document.org_id, document.document_id
        )
        files = self._files(doc_prefix, document, chunks)
        existing = await self._keys(doc_prefix)
        await self._each(
            self._storage.upload(self._config.bucket, key, body, content_type)
            for key, (body, content_type) in files.items()
            if key not in existing
        )
        await self._each(
            self._storage.delete(self._config.bucket, key)
            for key in existing - files.keys()
        )

    @override
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete every file and sidecar of the document, of every version.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document.

        """
        doc_prefix = document_prefix(self._config.prefix, org_id, document_id)
        keys = await self._keys(doc_prefix)
        await self._each(self._storage.delete(self._config.bucket, key) for key in keys)

    @override
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` in every sidecar of the document.

        The unit files are not touched.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document.
            fields: The metadata fields to set.

        """
        doc_prefix = document_prefix(self._config.prefix, org_id, document_id)
        keys = await self._keys(doc_prefix)
        sidecars = [key for key in keys if key.endswith(SIDECAR_SUFFIX)]
        await self._each(self._rewrite(key, fields) for key in sidecars)

    def _files(
        self, doc_prefix: str, document: SinkDocument, chunks: Sequence[ChunkPayload]
    ) -> dict[str, tuple[bytes, str]]:
        """Return each object to write (key → body and content type).

        Units and their sidecars.

        Args:
            doc_prefix: The document's key prefix.
            document: The document.
            chunks: Its chunks.

        Returns:
            Each object to write (key → body and content type).

        """
        files: dict[str, tuple[bytes, str]] = {}
        made = units(
            document,
            chunks,
            layout=self._config.layout,
            org_field=self._org_field,
            max_unit_bytes=self._config.max_unit_bytes,
        )
        for n, unit in enumerate(made):
            key = unit_key(doc_prefix, document.content_hash, n)
            files[key] = (unit.text.encode(), _TEXT)
            files[key + SIDECAR_SUFFIX] = (sidecar(unit.attributes), _JSON)
        return files

    async def _rewrite(self, key: str, fields: Mapping[str, str]) -> None:
        """Merge ``fields`` into one sidecar's attributes and write it back.

        Args:
            key: The sidecar's key.
            fields: The fields to set.

        """
        body = cast(
            "dict[str, object]",
            json.loads(await self._storage.download(self._config.bucket, key)),
        )
        attributes = cast("dict[str, object]", body.get("metadataAttributes", {}))
        await self._storage.upload(
            self._config.bucket, key, sidecar({**attributes, **fields}), _JSON
        )

    async def _keys(self, doc_prefix: str) -> set[str]:
        """Return the keys of every object under the document's prefix.

        Args:
            doc_prefix: The document's key prefix.

        Returns:
            The keys of every object under the document's prefix.

        """
        listed = await self._storage.list_objects(self._config.bucket, doc_prefix)
        return {o.key for o in listed}

    async def _each(self, calls: Iterable[Awaitable[None]]) -> None:
        """Await ``calls`` with at most ``concurrency`` in flight.

        Args:
            calls: The writes or deletes to run.

        """

        async def bounded(call: Awaitable[None]) -> None:
            async with self._limit:
                await call

        _ = await asyncio.gather(*(bounded(call) for call in calls))
