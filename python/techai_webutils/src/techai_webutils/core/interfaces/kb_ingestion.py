"""Knowledge-base ingestion interface (Bedrock StartIngestionJob / GetIngestionJob model).

Distinct from the retrieval-oriented ``knowledge_base.py`` (chunk/index/query): this
models the *ingestion job* lifecycle a caller drives — start a sync job, then poll it to
completion — and the direct path that skips the job: ingest or delete up to
``MAX_DIRECT_DOCUMENTS`` named documents per call (Bedrock
``IngestKnowledgeBaseDocuments`` / ``DeleteKnowledgeBaseDocuments``). Implementations: a
no-op stub (dev, no Bedrock emulator) and a real Bedrock client (stage/prod), selected by
a config-keyed factory.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import InvalidInputError
from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Collection, Sequence

MAX_DIRECT_DOCUMENTS = 25
"""The most documents one ``ingest_documents`` or ``delete_documents`` call accepts.

Every backend refuses a larger batch with ``InvalidInputError`` before any I/O, so a
caller sizes its batches by this one number whatever the Kind."""


class IngestionJobState(StrEnum):
    """Lifecycle state of a knowledge-base ingestion job.

    The string values mirror AWS Bedrock's ``ingestionJob.status`` values verbatim — the
    full set ``STARTING | IN_PROGRESS | COMPLETE | FAILED | STOPPING | STOPPED`` — so a
    raw status maps to a member with ``IngestionJobState(status)`` (``job_from_payload``
    coerces an *unknown* status to ``FAILED``).
    """

    STARTING = "STARTING"
    """The sync job has been accepted but has not begun processing."""
    IN_PROGRESS = "IN_PROGRESS"
    """The job is actively indexing the data source (non-terminal; keep polling)."""
    COMPLETE = "COMPLETE"
    """The job finished successfully — terminal."""
    FAILED = "FAILED"
    """The job failed, or was mapped from an unknown status / poll timeout — terminal; see
    ``error``."""
    STOPPING = "STOPPING"
    """A stop was requested but the job is still winding down — NON-terminal: it still
    holds Bedrock's ingestion lock, so the poller must keep polling until it reaches
    STOPPED."""
    STOPPED = "STOPPED"
    """The job was stopped (by an operator or a supervisor stopping a stuck job) and is
    fully wound down — terminal."""


@dataclass(frozen=True, slots=True)
class IngestionJob:
    """A handle to a knowledge-base ingestion job and its current state."""

    job_id: str
    """Provider-assigned job identifier used to poll the job to completion."""
    state: IngestionJobState
    """Current lifecycle state; ``is_terminal`` tells the poller when to stop."""
    error: str = ""
    """Failure detail when ``state is FAILED`` (empty on success or when no reason was
    reported)."""
    documents_scanned: int = 0
    """How many source documents the job has scanned so far (0 when the provider reports
    none); a count that moves between polls tells a slow job from a wedged one."""
    documents_failed: int = 0
    """How many documents the job failed to index (0 when the provider reports none). A
    COMPLETE job can still carry failures; this count is the signal to look them up."""

    @property
    def is_terminal(self) -> bool:
        """Whether the job has finished (COMPLETE, FAILED, or STOPPED)."""
        return self.state in {
            IngestionJobState.COMPLETE,
            IngestionJobState.FAILED,
            IngestionJobState.STOPPED,
        }


POLL_TIMEOUT_ERROR = "poll_timeout"
"""The ``IngestionJob.error`` sentinel a poller sets when its CLIENT-SIDE deadline elapses
first.

The job is FAILED-by-timeout, not FAILED by Bedrock, and may STILL be running server-side.
Shared so the poller (which produces it) and a caller that tracks the job (which must keep
it ``running`` and reattach later, not race a conflicting start) agree on the exact value
instead of duplicating a magic string. """


@dataclass(frozen=True, slots=True)
class KnowledgeBaseDocument:
    """One document in a KB data source, from Bedrock ``ListKnowledgeBaseDocuments``.

    A caller matches ``s3_uri`` back to its own document record and maps ``status`` to its
    own document status. ``status`` mirrors Bedrock's per-document status verbatim
    (``INDEXED``, ``FAILED``, ``IN_PROGRESS``, …); ``status_reason`` carries the failure
    detail when present.
    """

    s3_uri: str
    """The source object URI (``s3://bucket/key``); empty for a non-S3 / unparseable
    identifier."""
    status: str
    """Bedrock's per-document status, verbatim (the consumer maps it to its own domain
    status)."""
    status_reason: str = ""
    """Failure/ignore detail when Bedrock reported one (empty otherwise)."""


@dataclass(frozen=True, slots=True)
class S3Document:
    """One S3 object to ingest directly, with its optional metadata sidecar."""

    uri: str
    """The source object URI (``s3://bucket/key``)."""
    metadata_uri: str | None = None
    """The URI of the object's ``.metadata.json`` sidecar; ``None`` sends no metadata."""


@dataclass(frozen=True, slots=True)
class DocumentIngestResult:
    """Bedrock's answer for one document of a direct ingest or delete call.

    ``status`` mirrors Bedrock's per-document status verbatim (``STARTING``,
    ``IN_PROGRESS``, ``INDEXED``, ``FAILED``, ``DELETING``, …); the call is asynchronous,
    so the final status is read later (``iter_cohort``).
    """

    uri: str
    """The document's source URI (``s3://bucket/key``); empty for a non-S3 identifier."""
    status: str
    """Bedrock's per-document status, verbatim."""
    status_reason: str = ""
    """Failure detail when Bedrock reported one (empty otherwise)."""


def check_direct_batch(count: int, what: str) -> None:
    """Refuse a direct ingest or delete batch larger than ``MAX_DIRECT_DOCUMENTS``.

    Args:
        count: How many items the call names.
        what: The items' name in the message (``documents``, ``uris``).

    Raises:
        InvalidInputError: ``count`` exceeds ``MAX_DIRECT_DOCUMENTS``.

    """
    if count > MAX_DIRECT_DOCUMENTS:
        msg = (
            f"direct knowledge-base call takes at most {MAX_DIRECT_DOCUMENTS} {what}, "
            f"got {count}"
        )
        raise InvalidInputError(msg)


class KnowledgeBaseIngestor(ManagedResource, ABC):
    """Drives knowledge-base ingestion jobs (start and poll).

    It also lists a data source's documents, and ingests or deletes named documents
    directly, without a job.
    """

    @abstractmethod
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, round_id: str = ""
    ) -> IngestionJob:
        """Start (or attach to) an ingestion job for the given KB data source.

        ``round_id`` names the caller's start round; a start retried with the same
        non-empty round id is idempotent (Bedrock's ``clientToken``), so a retry never
        launches a second job. Empty (the default) sends no token, as before.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            round_id: The sync round, so a retried start attaches to the job it already
                started; empty starts a new job.

        Returns:
            The started (or attached) job.

        """
        ...

    @abstractmethod
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Return the current state of a previously-started ingestion job.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to read.

        Returns:
            The job's current state and statistics.

        """
        ...

    @abstractmethod
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List every document in the KB data source, across all pages.

        Each comes with its per-document status.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.

        Returns:
            Every document in the data source with its status.

        """
        ...

    async def iter_cohort(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        s3_uris: Collection[str],
    ) -> AsyncIterator[KnowledgeBaseDocument]:
        """Yield the KB documents of ``s3_uris``, the cohort a sync round started.

        This default lists the whole data source once and yields the cohort's documents; a
        Kind that can look documents up by URI overrides it. A URI the data source does
        not hold is not yielded.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            s3_uris: The source objects the sync round started with.

        Yields:
            The KB documents of ``s3_uris``, the cohort a sync round started.

        """
        wanted = set(s3_uris)
        if not wanted:
            return
        for doc in await self.list_documents(
            knowledge_base_id=knowledge_base_id, data_source_id=data_source_id
        ):
            if doc.s3_uri in wanted:
                yield doc

    @abstractmethod
    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Request a stop of an in-flight ingestion job.

        Returns the job in its ``STOPPING`` state.

        The recovery for a genuinely-stuck job — Bedrock has no force-unlock, so a wedged
        job must be actively stopped. STOPPING is non-terminal (the job still holds
        Bedrock's ingestion lock until it winds down to STOPPED), so the caller reattaches
        and polls it to STOPPED.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to stop.

        Returns:
            The job's state after the stop request.

        """
        ...

    @abstractmethod
    async def ingest_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        documents: Sequence[S3Document],
    ) -> list[DocumentIngestResult]:
        """Ingest up to ``MAX_DIRECT_DOCUMENTS`` S3 documents directly, without a job.

        Ingestion is asynchronous: each result carries the document's status when the
        call returned; read the final status later. Ingesting a document again replaces
        it, so a retried call is safe.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            documents: The documents, each with its optional metadata sidecar.

        Returns:
            One result per document Bedrock reported.

        Raises:
            InvalidInputError: More than ``MAX_DIRECT_DOCUMENTS`` documents.

        """
        ...

    @abstractmethod
    async def delete_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        uris: Sequence[str],
    ) -> list[DocumentIngestResult]:
        """Delete up to ``MAX_DIRECT_DOCUMENTS`` S3 documents directly, without a job.

        Deletion is asynchronous, like ``ingest_documents``; deleting a document that is
        already gone is safe to retry.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            uris: The documents' source URIs (``s3://bucket/key``).

        Returns:
            One result per document Bedrock reported.

        Raises:
            InvalidInputError: More than ``MAX_DIRECT_DOCUMENTS`` URIs.

        """
        ...


class ReattachableIngestor(KnowledgeBaseIngestor, ABC):
    """A ``KnowledgeBaseIngestor`` that can also poll a started job.

    It polls an ALREADY-started job to terminal: the reattach seam.

    Composes with ``KnowledgeBaseIngestor`` (ARCHITECTURE.md#interface-composition) rather
    than standing alone: it adds only ``poll_ingestion_job``, the capability a caller
    needs to converge either a job it just started OR one whose ``job_id`` it recorded
    earlier, without a second ``start`` that Bedrock would reject with
    ``ConflictException``. The single implementation is ``PollingIngestor`` (the decorator
    that owns the poll loop); base ingestors stay plain ``KnowledgeBaseIngestor``.
    """

    @abstractmethod
    async def poll_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Poll an already-started ingestion job (by ``job_id``).

        It polls until the job reaches a terminal state.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to wait on.

        Returns:
            The job once it reaches a terminal state.

        """
        ...
