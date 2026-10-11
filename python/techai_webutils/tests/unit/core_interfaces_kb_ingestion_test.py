"""Tests for the knowledge-base ingestion interface + IngestionJob."""

import pytest

from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseIngestor,
)


class TestIngestionJob:
    """Tests for the ingestion job."""

    def test_is_terminal_reflects_completion(self) -> None:
        """Test that is_terminal is True only for the terminal states (COMPLETE/FAILED/STOPPED).

        **Why this test is important:**
          - The batched KB-sync loop polls until terminal; a wrong terminal check would either spin
            forever (never terminal) or stop mid-sync (terminal too early), corrupting document status.
            STOPPING in particular is NON-terminal — treating it as terminal releases the single-writer
            lock while Bedrock still has an active job, causing a StartIngestionJob conflict loop.

        **What it tests:**
          - COMPLETE, FAILED, and STOPPED are terminal; STARTING, IN_PROGRESS, and STOPPING are not.
        """
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.COMPLETE).is_terminal is True
        )
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.FAILED).is_terminal is True
        )
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.STOPPED).is_terminal is True
        )
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.STARTING).is_terminal
            is False
        )
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.IN_PROGRESS).is_terminal
            is False
        )
        assert (
            IngestionJob(job_id="j", state=IngestionJobState.STOPPING).is_terminal
            is False
        )


class TestKnowledgeBaseIngestor:
    """Tests for the knowledge base ingestor."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that the ingestor ABC cannot be instantiated without the job methods.

        **Why this test is important:**
          - An instantiable stub would accept start/poll calls and silently no-op, so documents
            would never actually reach the vector store.

        **What it tests:**
          - Instantiating KnowledgeBaseIngestor directly raises TypeError.
        """
        with pytest.raises(TypeError):
            KnowledgeBaseIngestor()  # type: ignore[abstract]
