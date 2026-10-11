"""Tests for the env-aware knowledge-base client factory (stub <-> bedrock)."""

import pytest

# Imported eagerly (unlike the factory, which lazy-imports it) so the bedrock-selection test can
# isinstance-check the concrete type; the test venv has aiobotocore, so this load is harmless here.
from techai_webutils.clients.kb_ingestion.bedrock import BedrockKnowledgeBaseIngestor
from techai_webutils.clients.kb_ingestion.builder import (
    KbConfig,
    KbKind,
    new_kb_ingestor_from_config,
)
from techai_webutils.clients.kb_ingestion.noop import StubKnowledgeBaseIngestor
from techai_webutils.core.interfaces.kb_ingestion import IngestionJobState


class TestKnowledgeBaseFactory:
    """Tests for the knowledge base factory."""

    def test_stub_kind_selects_stub_client(self) -> None:
        """Test that kind=stub builds the no-op ingestor (dev, no Bedrock emulator).

        **Why this test is important:**
          - Dev must run the full pipeline without AWS; selecting the stub by config is the seam
            that makes local ingestion work, mirroring foundation/logger's kind factory.

        **What it tests:**
          - new_kb_ingestor_from_config(kind=STUB) returns a StubKnowledgeBaseIngestor.
        """
        ingestor = new_kb_ingestor_from_config(KbConfig(kind=KbKind.STUB))
        assert isinstance(ingestor, StubKnowledgeBaseIngestor)

    def test_bedrock_kind_selects_bedrock_client(self) -> None:
        """Test that kind=bedrock builds the real Bedrock ingestor (stage/prod).

        **Why this test is important:**
          - Stage/prod must use real Bedrock; the same config seam flips to the real client without
            any code change.

        **What it tests:**
          - new_kb_ingestor_from_config(kind=BEDROCK) returns a BedrockKnowledgeBaseIngestor.
        """
        ingestor = new_kb_ingestor_from_config(
            KbConfig(kind=KbKind.BEDROCK, region="us-east-1")
        )
        assert isinstance(ingestor, BedrockKnowledgeBaseIngestor)

    def test_unknown_kind_raises(self) -> None:
        """Test that an unrecognised kind raises a clear error (mirrors logger.NewFromConfig).

        **Why this test is important:**
          - A misconfigured kind must fail loudly at startup, not silently degrade to a wrong client.

        **What it tests:**
          - A bogus kind value raises ValueError naming the kind.
        """
        with pytest.raises(ValueError, match="knowledge-base kind"):
            new_kb_ingestor_from_config(KbConfig(kind="bogus"))  # type: ignore[arg-type]

    @pytest.mark.asyncio
    async def test_stub_completes_job_immediately(self) -> None:
        """Test that the stub ingestor reports jobs COMPLETE immediately.

        **Why this test is important:**
          - The local pipeline must reach status 'indexed' without a real KB; the stub completing
            instantly is what lets the end-to-end dev flow finish.

        **What it tests:**
          - start_ingestion_job returns a COMPLETE, terminal job.
        """
        ingestor = StubKnowledgeBaseIngestor()
        job = await ingestor.start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )
        assert job.state is IngestionJobState.COMPLETE
        assert job.is_terminal
