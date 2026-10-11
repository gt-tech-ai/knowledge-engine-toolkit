"""Tests for the KB ingestor's per-document listing (Bedrock ListKnowledgeBaseDocuments).

Covers the pure payload mapper, the Bedrock impl's pagination (mocked aiobotocore client), the stub's
empty listing, and — critically — that ``list_documents`` delegates through both the
``PollingIngestor`` and ``RetryingIngestor`` decorators so the composed stack still instantiates.
"""

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from techai_webutils.clients.kb_ingestion.bedrock.ingestor import (
    BedrockKnowledgeBaseIngestor,
)
from techai_webutils.clients.kb_ingestion.decorators import (
    PollingIngestor,
    RetryingIngestor,
)
from techai_webutils.clients.kb_ingestion.mapping import kb_document_from_payload
from techai_webutils.clients.kb_ingestion.noop.ingestor import StubKnowledgeBaseIngestor
from techai_webutils.core.interfaces.kb_ingestion import KnowledgeBaseDocument


class TestKbDocumentFromPayload:
    """The pure ListKnowledgeBaseDocuments detail → KnowledgeBaseDocument mapper."""

    def test_maps_s3_uri_status_and_reason(self) -> None:
        """Test that an S3-identified document detail maps to its uri + status + reason.

        **Why this test is important:**
          - This is the pure mapping a caller depends on to match a KB document back to its own
            document record; a wrong field would silently break every status write-back.

        **What it tests:**
          - identifier.s3.uri, status, and statusReason are lifted verbatim onto KnowledgeBaseDocument.
        """
        detail: dict[str, object] = {
            "status": "INDEXED",
            "identifier": {"dataSourceType": "S3", "s3": {"uri": "s3://b/docs/f.pdf"}},
            "statusReason": "",
        }
        assert kb_document_from_payload(detail) == KnowledgeBaseDocument(
            s3_uri="s3://b/docs/f.pdf", status="INDEXED", status_reason=""
        )

    def test_non_s3_or_missing_identifier_yields_empty_uri(self) -> None:
        """Test that a non-S3 / missing identifier degrades to an empty uri, never raising.

        **Why this test is important:**
          - A caller skips documents whose uri it cannot parse; the mapper must not crash on a
            custom/foreign identifier shape, it must hand back an empty uri to be skipped.

        **What it tests:**
          - A detail with a non-S3 identifier (and one with no identifier) maps to s3_uri == "".
        """
        custom: dict[str, object] = {
            "status": "FAILED",
            "identifier": {"dataSourceType": "CUSTOM", "custom": {"id": "x"}},
        }
        assert not kb_document_from_payload(custom).s3_uri
        assert not kb_document_from_payload({"status": "FAILED"}).s3_uri


class TestBedrockListDocuments:
    """BedrockKnowledgeBaseIngestor.list_documents pages through ListKnowledgeBaseDocuments."""

    @pytest.mark.asyncio
    async def test_pages_through_all_documents(self) -> None:
        """Test that list_documents follows nextToken and returns every document across pages.

        **Why this test is important:**
          - A KB data source can hold thousands of documents across many pages; stopping after page
            one would leave the caller blind to most documents' terminal status.

        **What it tests:**
          - Two response pages (nextToken then none) yield all three KnowledgeBaseDocuments, and the
            second call carries the first page's nextToken.
        """
        page1 = {
            "documentDetails": [
                {
                    "status": "INDEXED",
                    "identifier": {"s3": {"uri": "s3://b/a"}},
                    "statusReason": "",
                },
                {
                    "status": "FAILED",
                    "identifier": {"s3": {"uri": "s3://b/b"}},
                    "statusReason": "bad",
                },
            ],
            "nextToken": "tok2",
        }
        page2 = {
            "documentDetails": [
                {
                    "status": "INDEXED",
                    "identifier": {"s3": {"uri": "s3://b/c"}},
                    "statusReason": "",
                },
            ],
        }
        fake_client = AsyncMock()
        fake_client.list_knowledge_base_documents.side_effect = [page1, page2]

        # The ingestor opens its bedrock-agent client through aiobotocore's session on first use;
        # mock that SDK boundary so the open yields the fake client.
        session = MagicMock()
        session.create_client.return_value.__aenter__.return_value = fake_client
        with patch("aiobotocore.session.get_session", return_value=session):
            ingestor = BedrockKnowledgeBaseIngestor(region="us-east-1")

        docs = await ingestor.list_documents(
            knowledge_base_id="kb1", data_source_id="ds1"
        )

        assert docs == [
            KnowledgeBaseDocument(s3_uri="s3://b/a", status="INDEXED", status_reason=""),
            KnowledgeBaseDocument(
                s3_uri="s3://b/b", status="FAILED", status_reason="bad"
            ),
            KnowledgeBaseDocument(s3_uri="s3://b/c", status="INDEXED", status_reason=""),
        ]
        assert fake_client.list_knowledge_base_documents.await_count == 2
        assert (
            fake_client.list_knowledge_base_documents.await_args_list[1].kwargs[
                "nextToken"
            ]
            == "tok2"
        )


class TestDecoratorDelegation:
    """list_documents delegates through both decorators, keeping the stack instantiable."""

    @pytest.mark.asyncio
    async def test_polling_over_retrying_delegates_list_documents(self) -> None:
        """Test that PollingIngestor(RetryingIngestor(inner)) instantiates and forwards list_documents.

        **Why this test is important:**
          - list_documents is an abstract method on KnowledgeBaseIngestor; if either decorator failed
            to implement it, composing the stack would raise TypeError at construction — the
            composed stack must build and delegate the call to the wrapped ingestor.

        **What it tests:**
          - The composed decorator stack constructs without a TypeError and its list_documents returns
            the wrapped stub's empty listing.
        """
        stacked = PollingIngestor(RetryingIngestor(StubKnowledgeBaseIngestor()))
        docs = await stacked.list_documents(knowledge_base_id="kb1", data_source_id="ds1")
        assert docs == []
