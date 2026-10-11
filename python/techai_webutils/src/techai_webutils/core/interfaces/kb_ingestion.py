"""Knowledge-base ingestion interface (Bedrock StartIngestionJob / GetIngestionJob model).

Distinct from the retrieval-oriented ``knowledge_base.py`` (chunk/index/query): this models the *ingestion
job* lifecycle a caller drives — start a sync job, then poll it to completion. Implementations: a no-op stub
(dev, no Bedrock emulator) and a real Bedrock client (stage/prod), selected by a config-keyed factory.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import StrEnum

from techai_webutils.core.interfaces.lifecycle import ManagedResource


class IngestionJobState(StrEnum):
    """Lifecycle state of a knowledge-base ingestion job.

    The string values mirror AWS Bedrock's ``ingestionJob.status`` values verbatim — the full set
    ``STARTING | IN_PROGRESS | COMPLETE | FAILED | STOPPING | STOPPED`` — so a raw status maps to a
    member with ``IngestionJobState(status)`` (``job_from_payload`` coerces an *unknown* status to
    ``FAILED``).
    """

    STARTING = "STARTING"
    """The sync job has been accepted but has not begun processing."""
    IN_PROGRESS = "IN_PROGRESS"
    """The job is actively indexing the data source (non-terminal; keep polling)."""
    COMPLETE = "COMPLETE"
    """The job finished successfully — terminal."""
    FAILED = "FAILED"
    """The job failed, or was mapped from an unknown status / poll timeout — terminal; see ``error``."""
    STOPPING = "STOPPING"
    """A stop was requested but the job is still winding down — NON-terminal: it still holds Bedrock's
    ingestion lock, so the poller must keep polling until it reaches STOPPED."""
    STOPPED = "STOPPED"
    """The job was stopped (by an operator or a supervisor stopping a stuck job) and is fully wound down —
    terminal."""


@dataclass(frozen=True, slots=True)
class IngestionJob:
    """A handle to a knowledge-base ingestion job and its current state."""

    job_id: str
    """Provider-assigned job identifier used to poll the job to completion."""
    state: IngestionJobState
    """Current lifecycle state; ``is_terminal`` tells the poller when to stop."""
    error: str = ""
    """Failure detail when ``state is FAILED`` (empty on success or when no reason was reported)."""

    @property
    def is_terminal(self) -> bool:
        """Whether the job has finished (COMPLETE, FAILED, or STOPPED)."""
        return self.state in {
            IngestionJobState.COMPLETE,
            IngestionJobState.FAILED,
            IngestionJobState.STOPPED,
        }


POLL_TIMEOUT_ERROR = "poll_timeout"
"""The ``IngestionJob.error`` sentinel a poller sets when its CLIENT-SIDE deadline elapses first.

The job is FAILED-by-timeout, not FAILED by Bedrock, and may STILL be running server-side. Shared so the
poller (which produces it) and a caller that tracks the job (which must keep it ``running`` and reattach
later, not race a conflicting start) agree on the exact value instead of duplicating a magic string.
"""


@dataclass(frozen=True, slots=True)
class KnowledgeBaseDocument:
    """One document in a KB data source, from Bedrock ``ListKnowledgeBaseDocuments``.

    A caller matches ``s3_uri`` back to its own document record and maps ``status`` to its own document
    status. ``status`` mirrors Bedrock's per-document status verbatim (``INDEXED``, ``FAILED``,
    ``IN_PROGRESS``, …); ``status_reason`` carries the failure detail when present.
    """

    s3_uri: str
    """The source object URI (``s3://bucket/key``); empty for a non-S3 / unparseable identifier."""
    status: str
    """Bedrock's per-document status, verbatim (the consumer maps it to its own domain status)."""
    status_reason: str = ""
    """Failure/ignore detail when Bedrock reported one (empty otherwise)."""


class KnowledgeBaseIngestor(ManagedResource, ABC):
    """Drives knowledge-base ingestion jobs (start + poll) and lists a data source's documents."""

    @abstractmethod
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str
    ) -> IngestionJob:
        """Start (or attach to) an ingestion job for the given KB data source."""
        ...

    @abstractmethod
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Return the current state of a previously-started ingestion job."""
        ...

    @abstractmethod
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List every document in the KB data source with its per-document status (all pages)."""
        ...

    @abstractmethod
    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Request a stop of an in-flight ingestion job; returns the job in its ``STOPPING`` state.

        The recovery for a genuinely-stuck job — Bedrock has no force-unlock, so a wedged job must be
        actively stopped. STOPPING is non-terminal (the job still holds Bedrock's
        ingestion lock until it winds down to STOPPED), so the caller reattaches and polls it to STOPPED.
        """
        ...


class ReattachableIngestor(KnowledgeBaseIngestor, ABC):
    """A ``KnowledgeBaseIngestor`` that can also poll an ALREADY-started job to terminal (the reattach seam).

    Composes with ``KnowledgeBaseIngestor`` (ARCHITECTURE.md#interface-composition) rather than standing
    alone: it adds only ``poll_ingestion_job``, the capability a caller needs to converge either a job it
    just started OR one whose ``job_id`` it recorded earlier, without a second ``start`` that Bedrock
    would reject with ``ConflictException``. The single implementation is ``PollingIngestor``
    (the decorator that owns the poll loop); base ingestors stay plain ``KnowledgeBaseIngestor``.
    """

    @abstractmethod
    async def poll_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Poll an already-started ingestion job (by ``job_id``) until it reaches a terminal state."""
        ...
