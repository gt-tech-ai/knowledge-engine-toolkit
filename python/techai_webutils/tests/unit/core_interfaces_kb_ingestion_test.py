"""Tests for the knowledge-base ingestion interface + IngestionJob."""

from typing import TYPE_CHECKING, cast

import pytest

from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseIngestor,
)

if TYPE_CHECKING:
    from collections.abc import Callable


class TestIngestionJob:
    """Tests for the ingestion job."""

    @staticmethod
    def test_is_terminal_reflects_completion() -> None:
        """Test that is_terminal is True only for the terminal states.

        Those are COMPLETE, FAILED and STOPPED.

        **Why this test is important:**
          - The batched KB-sync loop polls until terminal; a wrong terminal check would
            either spin forever (never terminal) or stop mid-sync (terminal too early),
            corrupting document status. STOPPING in particular is NON-terminal — treating
            it as terminal releases the single-writer lock while Bedrock still has an
            active job, causing a StartIngestionJob conflict loop.

        **What it tests:**
          - COMPLETE, FAILED, and STOPPED are terminal; STARTING, IN_PROGRESS, and
            STOPPING are not.
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

    @staticmethod
    def test_cannot_instantiate_abc() -> None:
        """Test that the ingestor ABC cannot be instantiated without the job methods.

        **Why this test is important:**
          - An instantiable stub would accept start/poll calls and silently no-op, so
            documents would never actually reach the vector store.

        **What it tests:**
          - Instantiating KnowledgeBaseIngestor directly raises TypeError.
        """
        with pytest.raises(TypeError):
            _ = cast("Callable[[], object]", KnowledgeBaseIngestor)()
