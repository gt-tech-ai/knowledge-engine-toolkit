"""Vector-backed KnowledgeBase (local dev chunk indexer): embed chunks → upsert into a VectorStore.

The local analogue of a managed knowledge base: instead of handing documents to a managed service,
it embeds each chunk (Ollama) and upserts it into a VectorStore (Qdrant), writing the retrieval
metadata (the consumer's ``attributes``, plus ``document_name`` and ``chunk_index``). It composes the
same ``VectorStore`` the retrieval engine reads, so an indexed document is immediately queryable
end-to-end.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.clients.vector.composition import require_matching_dimension
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.knowledge_base import (
    KBDocument,
    KBSyncResult,
    KnowledgeBase,
)
from techai_webutils.core.interfaces.vector_store import VectorEntry
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import VectorStore

logger = get_logger(__name__)

# Embed + upsert one document a fixed-size sub-batch at a time, instead of embedding EVERY
# chunk up front and doing one all-or-nothing upsert — on the CPU-Ollama path that single giant unit
# exceeds the embed read timeout and the whole (thousands-of-chunk) document is lost and redriven from
# zero forever. Sub-batches are processed SEQUENTIALLY, not concurrently: this KnowledgeBase is the
# local Ollama+Qdrant path, and Ollama is a single CPU-bound backend, so concurrent embed calls do not
# parallelize — they make each HTTP request wait behind the others and blow past its read timeout. One
# in-flight embed gives each call the whole backend.
_EMBED_SUB_BATCH = 96
"""Number of chunks embedded + upserted per sub-batch, bounding a document's per-unit embed work."""

_RESERVED_ATTRIBUTE_KEYS = frozenset({
    "document_id",
    "content",
    "entry_id",
    "document_name",
    "chunk_index",
})
"""Metadata keys the index writes itself (the entry's id/document/content, the name and chunk index);
an attribute with one of these names would overwrite it in the stored payload, so it is refused."""


class VectorKnowledgeBase(NoOpAsyncResource, KnowledgeBase):
    """KnowledgeBase over an EmbeddingProvider + VectorStore (embed chunks → upsert; count for status)."""

    def __init__(
        self, embedder: EmbeddingProvider, store: VectorStore, collection: str
    ) -> None:
        """Compose the embedder + store; fail loudly if their vector dimensions disagree."""
        require_matching_dimension(embedder, store, collection)
        self._embedder = embedder
        self._store = store
        self._collection = collection

    async def index_document(
        self,
        document_id: str,
        chunks: list[str],
        *,
        attributes: Mapping[str, str] | None = None,
        document_name: str = "",
    ) -> None:
        """Embed + upsert one point per chunk, a sub-batch at a time, resuming past indexed sub-batches.

        Each sub-batch is embedded and upserted before the next, so peak memory is O(sub-batch) not
        O(document) and every completed sub-batch is durable. Before embedding a sub-batch its stable
        ``{document_id}:{index}`` ids are probed against the store: a sub-batch whose chunks are ALL
        present with exactly the metadata this call would write is skipped (checkpoint resume), so an
        at-least-once redrive — or a mid-ingest restart — re-embeds only what is missing instead of
        redoing the expensive CPU embedding from zero. A sub-batch that is partially present, or stored
        with other attributes or another name (a reclassified or renamed document), is re-embedded and
        re-upserted (idempotent), so its stored scope always matches the latest call. ``attributes`` are
        stamped onto every chunk; ``document_name`` is stamped for citation display, falling back to
        ``document_id`` when empty.

        Raises:
            ValueError: An attribute is named like a key the index writes itself (``document_id``,
                ``content``, ``entry_id``, ``document_name``, ``chunk_index``).

        """
        base_metadata = dict(attributes or {})
        if reserved := sorted(_RESERVED_ATTRIBUTE_KEYS.intersection(base_metadata)):
            msg = f"attributes may not use the reserved metadata keys {reserved}"
            raise AppValueError(msg)
        if not chunks:
            return
        name = document_name or document_id
        sub_batches = [
            chunks[i : i + _EMBED_SUB_BATCH]
            for i in range(0, len(chunks), _EMBED_SUB_BATCH)
        ]
        total = len(sub_batches)
        logger.info(
            "vector index: start",
            document_id=document_id,
            chunks=len(chunks),
            sub_batches=total,
        )
        base = 0
        skipped = 0
        for number, batch in enumerate(sub_batches, start=1):
            ids = [f"{document_id}:{base + offset}" for offset in range(len(batch))]
            # Prefer the source name so a retrieval citation displays it; fall back to the document_id
            # when a caller passes no name.
            metadata = [
                {
                    **base_metadata,
                    "document_name": name,
                    "chunk_index": str(base + offset),
                }
                for offset in range(len(batch))
            ]
            stored = await self._store.stored_metadata(self._collection, ids)
            if all(
                stored.get(chunk_id) == expected
                for chunk_id, expected in zip(ids, metadata, strict=True)
            ):
                base += len(batch)
                skipped += 1
                continue
            embeddings = await self._embedder.embed_batch(batch)
            # The positional zip below assumes one embedding per chunk; a provider that returns a short
            # (or over-long) list would otherwise IndexError mid-loop or silently drop chunks. Fail fast
            # and loud so a partial embed is a clear error, not a corrupt index.
            if len(embeddings) != len(batch):
                msg = f"embedder returned {len(embeddings)} embeddings for {len(batch)} chunks"
                raise AppValueError(msg)
            entries = [
                VectorEntry(
                    id=ids[offset],
                    document_id=document_id,
                    content=batch[offset],
                    embedding=embeddings[offset].embedding,
                    metadata=metadata[offset],
                )
                for offset in range(len(batch))
            ]
            await self._store.upsert(self._collection, entries)
            base += len(batch)
            logger.info(
                "vector index: sub-batch upserted",
                document_id=document_id,
                done=number,
                total=total,
            )
        logger.info(
            "vector index: done",
            document_id=document_id,
            points=len(chunks),
            skipped=skipped,
        )

    async def remove_document(self, document_id: str) -> None:
        """Remove every chunk of the document from the store."""
        await self._store.delete_by_document(self._collection, document_id)

    async def get_document_status(self, document_id: str) -> KBDocument | None:
        """Report ``ready`` + the chunk count if the document has points, else ``None`` (not indexed)."""
        count = await self._store.count_by_document(self._collection, document_id)
        if count == 0:
            return None
        return KBDocument(
            id=document_id,
            file_name=document_id,
            content_type="text/plain",
            status="ready",
            chunk_count=count,
        )

    @override
    async def sync(self) -> KBSyncResult:
        """No-op sync: upserts are synchronous in the vector store, so there is nothing to reconcile."""
        return KBSyncResult(
            documents_added=0,
            documents_updated=0,
            documents_removed=0,
            chunks_created=0,
            errors=[],
        )
