"""No-op Knowledge Base ingestor for dev/local (no Bedrock emulator).

Jobs complete immediately so the full ingestion pipeline (parse -> metadata -> sidecar ->
status ``indexed``) runs end-to-end locally without AWS. Records started jobs for
assertions.
"""

from __future__ import annotations

from typing import final, override

from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseDocument,
    KnowledgeBaseIngestor,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


@final
class StubKnowledgeBaseIngestor(NoOpAsyncResource, KnowledgeBaseIngestor):
    """A KnowledgeBaseIngestor whose jobs are instantly COMPLETE (dev/local)."""

    def __init__(self) -> None:
        """Start with an empty record of started + stopped jobs and a job-id counter."""
        self.started: list[tuple[str, str, str]] = []
        self.stopped: list[tuple[str, str, str]] = []
        self._counter = 0

    @override
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, round_id: str = ""
    ) -> IngestionJob:
        """Record and immediately complete a synthetic ingestion job.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            round_id: The sync round, so a retried start attaches to the job it already
                started; empty starts a new job.

        Returns:
            The completed job.

        """
        self._counter += 1
        job_id = f"stub-job-{self._counter}"
        self.started.append((knowledge_base_id, data_source_id, job_id))
        return IngestionJob(job_id=job_id, state=IngestionJobState.COMPLETE)

    @override
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Return a COMPLETE job for any id (stub jobs finish on start).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to read.

        Returns:
            A COMPLETE job for any id (stub jobs finish on start).

        """
        return IngestionJob(job_id=job_id, state=IngestionJobState.COMPLETE)

    @override
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """Return no documents — the stub has no real KB to list (dev/local).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.

        Returns:
            No documents — the stub has no real KB to list (dev/local).

        """
        return []

    @override
    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Record the stop request and return a STOPPING job.

        (dev/local; no real Bedrock).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to stop.

        Returns:
            The job after the stop request.

        """
        self.stopped.append((knowledge_base_id, data_source_id, job_id))
        return IngestionJob(job_id=job_id, state=IngestionJobState.STOPPING)
