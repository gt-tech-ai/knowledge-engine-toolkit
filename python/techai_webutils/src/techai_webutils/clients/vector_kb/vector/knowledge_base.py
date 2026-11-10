"""Vector-backed KnowledgeBase (local dev chunk indexer).

It embeds chunks and upserts them into a VectorStore.

The local analogue of a managed knowledge base: instead of handing documents to a managed
service, it embeds each chunk (Ollama) and upserts it into a VectorStore (Qdrant), writing
the retrieval metadata (the consumer's ``attributes``, plus ``document_name`` and
``chunk_index``). It composes the same ``VectorStore`` the retrieval engine reads, so an
indexed document is immediately queryable end-to-end.
"""

from __future__ import annotations

import asyncio
from itertools import starmap
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.knowledge_base import (
    KBDocument,
    KBSyncResult,
    KnowledgeBase,
)
from techai_webutils.core.interfaces.vector_store import VectorEntry
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.logger import get_logger
from techai_webutils.foundation.vectors import require_matching_dimension

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.vector_store import VectorStore

logger = get_logger(__name__)

# Embed + upsert one document a fixed-size sub-batch at a time, instead of embedding EVERY
# chunk up front and doing one all-or-nothing upsert — on the CPU-Ollama path that single
# giant unit exceeds the embed read timeout and the whole (thousands-of-chunk) document is
# lost and redriven from zero forever. By default sub-batches run SEQUENTIALLY
# (``embed_concurrency=1``): on the local Ollama+Qdrant path Ollama is a single CPU-bound
# backend, so concurrent embed calls do not parallelize — they make each HTTP request wait
# behind the others and blow past its read timeout. A hosted embedder that serves requests
# in parallel raises ``embed_concurrency`` to keep that many sub-batches in flight; it is
# a static cap (backing off on throttling is the caller's stage's job).
_EMBED_SUB_BATCH = 96
"""Number of chunks embedded + upserted per sub-batch, bounding a document's per-unit
embed work."""

_RESERVED_ATTRIBUTE_KEYS = frozenset({
    "document_id",
    "content",
    "entry_id",
    "document_name",
    "chunk_index",
})
"""Metadata keys the index writes itself (the entry's id/document/content, the name and
chunk index); an attribute with one of these names would overwrite it in the stored
payload, so it is refused."""


def _validated_attributes(attributes: Mapping[str, str] | None) -> dict[str, str]:
    """Return a copy of the consumer's chunk attributes.

    One named like a key the index writes is refused.

    Args:
        attributes: The caller's attributes; ``None`` is none.

    Returns:
        A copy of the consumer's chunk attributes.

    A callee raises ``ValueError``: An attribute uses a reserved metadata key.

    Raises:
        AppValueError: Attributes may not use the reserved metadata keys ``reserved``.

    """
    metadata = dict(attributes or {})
    if reserved := sorted(_RESERVED_ATTRIBUTE_KEYS.intersection(metadata)):
        msg = f"attributes may not use the reserved metadata keys {reserved}"
        raise AppValueError(msg)
    return metadata


class VectorKnowledgeBase(NoOpAsyncResource, KnowledgeBase):
    """KnowledgeBase over an EmbeddingProvider and a VectorStore.

    It embeds chunks and upserts them; the count gives the status.
    """

    def __init__(
        self,
        embedder: EmbeddingProvider,
        store: VectorStore,
        collection: str,
        *,
        embed_concurrency: int = 1,
    ) -> None:
        """Compose the embedder + store; fail loudly if their vector dimensions disagree.

        ``embed_concurrency`` is how many sub-batches are embedded and upserted at once
        (1: one at a time, as before).

        A callee raises ``ValueError``: The dimensions disagree, or ``embed_concurrency``
        is below 1.

        Args:
            embedder: Embeds the chunks.
            store: Holds the vectors.
            collection: The store's collection.
            embed_concurrency: How many sub-batches are embedded at once.

        Raises:
            AppValueError: Embed_concurrency must be at least 1, got
                ``embed_concurrency``.

        """
        require_matching_dimension(embedder, store, collection)
        if embed_concurrency < 1:
            msg = f"embed_concurrency must be at least 1, got {embed_concurrency}"
            raise AppValueError(msg)
        self._embedder: EmbeddingProvider = embedder
        self._store: VectorStore = store
        self._collection: str = collection
        self._embed_concurrency: int = embed_concurrency

    @override
    async def index_document(
        self,
        document_id: str,
        chunks: list[str],
        *,
        attributes: Mapping[str, str] | None = None,
        document_name: str = "",
    ) -> None:
        """Embed and upsert one point per chunk, a sub-batch at a time.

        Already-indexed sub-batches are skipped on resume.

        Each sub-batch is embedded and upserted before the next, so peak memory is
        O(sub-batch) and every completed sub-batch is durable; a sub-batch already stored
        as this call would write it is skipped (see ``_index_sub_batch``). ``attributes``
        are stamped onto every chunk; ``document_name`` is stamped for citation display,
        falling back to ``document_id``.

        A callee raises ``ValueError``: An attribute is named like a key the index writes
        itself (``document_id``, ``content``, ``entry_id``, ``document_name``,
        ``chunk_index``).

        Args:
            document_id: The document's identifier; re-indexing replaces its chunks.
            chunks: The document's chunk texts, in order.
            attributes: Metadata stamped on every chunk (for example a tenant or
                sensitivity label); ``None`` stamps none.
            document_name: The display name stored with each chunk; empty falls back to
                ``document_id``.

        """
        base_metadata = _validated_attributes(attributes)
        if not chunks:
            return
        base_metadata["document_name"] = document_name or document_id
        skipped = await self._index_sub_batches(document_id, chunks, base_metadata)
        logger.info(
            "vector index: done",
            document_id=document_id,
            points=len(chunks),
            skipped=skipped,
        )

    async def _index_sub_batches(
        self, document_id: str, chunks: list[str], base_metadata: Mapping[str, str]
    ) -> int:
        """Index ``chunks`` sub-batch by sub-batch, ``embed_concurrency`` at once.

        Returns how many sub-batches were skipped.

        Args:
            document_id: The document.
            chunks: Its chunk texts.
            base_metadata: Metadata every chunk carries.

        Returns:
            The number of sub-batches that failed.

        """
        starts = range(0, len(chunks), _EMBED_SUB_BATCH)
        logger.info(
            "vector index: start",
            document_id=document_id,
            chunks=len(chunks),
            sub_batches=len(starts),
        )
        gate = asyncio.Semaphore(self._embed_concurrency)

        async def run(number: int, base: int) -> bool:
            batch = chunks[base : base + _EMBED_SUB_BATCH]
            metadata = [
                {**base_metadata, "chunk_index": str(index)}
                for index in range(base, base + len(batch))
            ]
            async with gate:
                written = await self._index_sub_batch(document_id, base, batch, metadata)
            if written:
                logger.info(
                    "vector index: sub-batch upserted",
                    document_id=document_id,
                    done=number,
                    total=len(starts),
                )
            return written

        written = await asyncio.gather(*starmap(run, enumerate(starts, start=1)))
        return written.count(False)

    async def _index_sub_batch(
        self,
        document_id: str,
        base: int,
        batch: list[str],
        metadata: list[dict[str, str]],
    ) -> bool:
        """Embed and upsert one sub-batch starting at chunk ``base``.

        A sub-batch already stored as is is skipped.

        The stable ``{document_id}:{index}`` ids are probed first: when every chunk is
        present with exactly ``metadata``, the sub-batch is skipped (checkpoint resume),
        so an at-least-once redrive or a mid-ingest restart re-embeds only what is
        missing. A sub-batch partially present, or stored with other attributes or another
        name (a reclassified or renamed document), is re-embedded and re-upserted
        (idempotent), so its stored scope always matches the latest call.

        A callee raises ``ValueError``: The embedder returned a different number of
        embeddings than chunks.

        Args:
            document_id: The document.
            base: The sub-batch's first chunk index.
            batch: The sub-batch's chunk texts.
            metadata: Each chunk's metadata.

        Returns:
            True when the sub-batch was embedded and upserted; False when every chunk was
                already stored with exactly ``metadata`` (a resumed redrive skips it).

        """
        ids = [f"{document_id}:{base + offset}" for offset in range(len(batch))]
        stored = await self._store.stored_metadata(self._collection, ids)
        if all(
            stored.get(chunk_id) == expected
            for chunk_id, expected in zip(ids, metadata, strict=True)
        ):
            return False
        entries = await self._embed_entries(document_id, ids, batch, metadata)
        await self._store.upsert(self._collection, entries)
        return True

    async def _embed_entries(
        self,
        document_id: str,
        ids: list[str],
        batch: list[str],
        metadata: list[dict[str, str]],
    ) -> list[VectorEntry]:
        """Embed ``batch`` and pair each chunk with its id, embedding and metadata.

        A callee raises ``ValueError``: The embedder returned a different number of
        embeddings than chunks; a short or over-long list would otherwise drop chunks
        silently or corrupt the index.

        Args:
            document_id: The document.
            ids: Each chunk's entry id.
            batch: The chunk texts.
            metadata: Each chunk's metadata.

        Returns:
            The entries to upsert.

        Raises:
            AppValueError: Embedder returned ``len(embeddings)`` embeddings for
                ``len(batch)`` chunks.

        """
        embeddings = await self._embedder.embed_batch(batch)
        if len(embeddings) != len(batch):
            msg = (
                f"embedder returned {len(embeddings)} embeddings for {len(batch)} chunks"
            )
            raise AppValueError(msg)
        return [
            VectorEntry(
                id=chunk_id,
                document_id=document_id,
                content=content,
                embedding=embedding.embedding,
                metadata=meta,
            )
            for chunk_id, content, embedding, meta in zip(
                ids, batch, embeddings, metadata, strict=True
            )
        ]

    @override
    async def remove_document(self, document_id: str) -> None:
        """Remove every chunk of the document from the store.

        Args:
            document_id: The document whose chunks are removed.

        """
        await self._store.delete_by_document(self._collection, document_id)

    @override
    async def get_document_status(self, document_id: str) -> KBDocument | None:
        """Report ``ready`` and the chunk count if the document has points.

        ``None`` when it is not indexed.

        Args:
            document_id: The document to look up.

        Returns:
            The document, or ``None`` when it has no points.

        """
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
        """No-op sync: upserts are synchronous in the vector store.

        There is nothing to reconcile.

        Returns:
            A result reporting no change.

        """
        return KBSyncResult(
            documents_added=0,
            documents_updated=0,
            documents_removed=0,
            chunks_created=0,
            errors=[],
        )
