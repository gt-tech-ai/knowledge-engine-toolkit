"""``VectorStoreSink``.

Write a document's chunks and vectors through an org-scoped vector store.

Each chunk becomes one entry with the stable id ``chunk_id(document, n)``
(``<document>:<n>``, ``n`` the chunk's ``ordinal`` when set, else its position in the
write), so a rewrite of the same document replaces its entries. Every entry stores the
document's metadata, its ``content_hash`` and embedding model, the chunk's structure
fields, and one ``entities.<label>`` list per label.

A write resumes: an entry already stored, where writes go, with exactly the metadata it
    would be
written with is not written again, so a redelivery after a partial write sends only the
rest. The metadata is the only comparison, so a caller puts whatever determines a chunk's
text (the pipeline's fingerprint) in the document metadata. When the document holds
entries the write does not (a previous version with more chunks), they are deleted after
the write.

Before writing, the sink checks the vectors were made by the model the index was built
    for: a
mismatch raises ``CONFLICT`` (permanent), even at equal dimensions, since mixing two
models' vectors in one index silently ruins its search.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError, ConflictError
from techai_webutils.core.interfaces.sink import Sink, chunk_id
from techai_webutils.core.interfaces.vector_store import VectorEntry
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.sink import (
        ChunkPayload,
        SinkDocument,
        SinkStore,
    )

ENTITY_SEPARATOR = "|"
"""Joins an entity label's values into one metadata string (the store splits it back)."""


class VectorStoreSink(NoOpAsyncResource, Sink):
    """A ``Sink`` over a ``SinkStore``.

    (the OpenSearch vector store) and one collection.
    """

    def __init__(
        self, store: SinkStore, collection: str, *, org_field: str = "org_id"
    ) -> None:
        """Write to ``collection`` of ``store``.

        ``org_field`` is where entries carry their org.
        """
        self._store = store
        self._collection = collection
        self._org_field = org_field

    @override
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Check the vectors and the model, upsert the entries not stored as is.

        Drop the rest.

        Raises:
            AppValueError: The vectors do not pair one-to-one with the chunks.
            ConflictError: The vectors come from another model than the index's.

        """
        if vectors is None or len(vectors) != len(chunks):
            count = "no" if vectors is None else len(vectors)
            msg = f"{count} vectors for {len(chunks)} chunks of {document.document_id}"
            raise AppValueError(msg)
        store = self._store.for_org(document.org_id)
        await self._check_model(store, document)
        entries = [
            self._entry(
                document, chunk, n if chunk.ordinal is None else chunk.ordinal, vector
            )
            for n, (chunk, vector) in enumerate(zip(chunks, vectors, strict=True))
        ]
        ids = [entry.id for entry in entries]
        stored = await store.stored_metadata(self._collection, ids)
        held = await store.count_by_document(self._collection, document.document_id)
        missing = [e for e in entries if stored.get(e.id) != e.metadata]
        if missing:
            await store.upsert(self._collection, missing)
        if held > len(stored):
            await store.delete_by_document(self._collection, document.document_id, ids)

    @override
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete the document's entries in its org."""
        await self._store.for_org(org_id).delete_by_document(
            self._collection, document_id
        )

    @override
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set ``fields`` on the document's entries in its org, without re-embedding."""
        await self._store.for_org(org_id).update_metadata(
            self._collection, document_id, fields
        )

    async def _check_model(self, store: SinkStore, document: SinkDocument) -> None:
        """Raise ``CONFLICT``.

        When the vectors' model is not the one the index is built for.
        """
        index = await store.current_index(self._collection)
        expected = (
            await store.index_model(index)
            if index is not None
            else store.configured_model
        )
        actual = (document.embedding_model, document.embedding_version)
        if actual != expected:
            msg = (
                f"vectors of {document.document_id} come from {actual}, "
                f"but the index is built for {expected}"
            )
            raise ConflictError(msg)

    def _entry(
        self,
        document: SinkDocument,
        chunk: ChunkPayload,
        n: int,
        vector: Sequence[float],
    ) -> VectorEntry:
        """Return chunk ``n``'s entry: its stable id, text, vector and stored metadata."""
        return VectorEntry(
            id=chunk_id(document.document_id, n),
            document_id=document.document_id,
            content=chunk.text,
            embedding=list(vector),
            metadata=self._metadata(document, chunk, n),
        )

    def _metadata(
        self, document: SinkDocument, chunk: ChunkPayload, n: int
    ) -> dict[str, str]:
        """Return the metadata stored on chunk ``n``."""
        metadata = {
            **document.metadata,
            **chunk.metadata,
            self._org_field: document.org_id,
            "content_hash": document.content_hash,
            "embedding_model": document.embedding_model,
            "embedding_version": document.embedding_version,
            "chunk_index": str(n),
        }
        for key in ("section_path", "element_type", "language"):
            if value := getattr(chunk, key):
                metadata[key] = str(value)
        for label, values in chunk.entities.items():
            if values:
                metadata[f"entities.{label}"] = ENTITY_SEPARATOR.join(values)
        return metadata
