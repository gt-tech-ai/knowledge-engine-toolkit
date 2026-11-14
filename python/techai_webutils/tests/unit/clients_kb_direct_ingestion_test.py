"""Tests for direct document ingestion and deletion on the KB ingestor.

Covers ``ingest_documents`` / ``delete_documents`` on the Bedrock backend (a real
aiobotocore ``bedrock-agent`` client under ``AioStubber``, which validates every request
and response against the pinned botocore service model), the noop backend, the 25-item
limit, and the ``RetryingIngestor`` / ``PollingIngestor`` decorators.
"""

from __future__ import annotations

from contextlib import AsyncExitStack, asynccontextmanager
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, create_autospec, patch

import aiobotocore.session
import pytest
from aiobotocore.stub import AioStubber

from techai_webutils.clients.kb_ingestion.bedrock.ingestor import (
    BedrockKnowledgeBaseIngestor,
)
from techai_webutils.clients.kb_ingestion.decorators import (
    PollingIngestor,
    RetryingIngestor,
)
from techai_webutils.clients.kb_ingestion.noop import StubKnowledgeBaseIngestor
from techai_webutils.core.errors import AppError, ErrorCode, InvalidInputError
from techai_webutils.core.interfaces.kb_ingestion import (
    MAX_DIRECT_DOCUMENTS,
    DocumentIngestResult,
    KnowledgeBaseIngestor,
    S3Document,
)

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator

    from aiobotocore.client import AioBaseClient

_KB = "KB12345678"
_DS = "DS12345678"
_MAX_PER_CALL = 10
_TWO_ATTEMPTS = 2


def _detail(uri: str, status: str, reason: str = "") -> dict[str, object]:
    """Return one ``documentDetails`` entry as Bedrock sends it.

    Args:
        uri: The document's S3 URI.
        status: Bedrock's per-document status.
        reason: The status reason; empty leaves the field out.

    Returns:
        The entry.

    """
    detail: dict[str, object] = {
        "knowledgeBaseId": _KB,
        "dataSourceId": _DS,
        "status": status,
        "identifier": {"dataSourceType": "S3", "s3": {"uri": uri}},
    }
    if reason:
        detail["statusReason"] = reason
    return detail


def _content(uri: str) -> dict[str, object]:
    """Return the S3 ``content`` block the ingest request carries for ``uri``.

    Args:
        uri: The document's S3 URI.

    Returns:
        The block.

    """
    return {"dataSourceType": "S3", "s3": {"s3Location": {"uri": uri}}}


@asynccontextmanager
async def _stubbed() -> AsyncGenerator[tuple[BedrockKnowledgeBaseIngestor, AioStubber]]:
    """Yield a Bedrock ingestor whose bedrock-agent client is stubbed.

    The client is a real aiobotocore client (static fake credentials, no network); the
    stubber answers each call in order and validates its parameters against the service
    model.

    Yields:
        The ingestor and the stubber that answers its calls.

    """
    real = aiobotocore.session.get_session()
    real.set_credentials("test", "test")
    async with AsyncExitStack() as stack:
        client: AioBaseClient = cast(
            "AioBaseClient",
            await stack.enter_async_context(
                real.create_client("bedrock-agent", region_name="us-east-1")
            ),
        )
        stubber = AioStubber(client)
        session = MagicMock()
        cast(
            "MagicMock",
            cast(
                "MagicMock", cast("MagicMock", session.create_client).return_value
            ).__aenter__,
        ).return_value = client
        with patch("aiobotocore.session.get_session", return_value=session):
            ingestor = BedrockKnowledgeBaseIngestor(region="us-east-1")
        with stubber:
            yield ingestor, stubber
        stubber.assert_no_pending_responses()


class TestBedrockDirectIngestion:
    """``BedrockKnowledgeBaseIngestor`` ingest and delete, against the service model."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_ingest_sends_s3_content_and_metadata_and_maps_results() -> None:
        """Test that an ingest sends each document's S3 content and sidecar location.

        **Why this test is important:**
          - Bedrock indexes exactly what the request names: a wrong content or metadata
            shape is a ``ValidationException`` in production, and a dropped metadata URI
            indexes the document without its attributes.

        **What it tests:**
          - the request carries ``content.s3.s3Location.uri`` for every document and
            ``metadata`` of type ``S3_LOCATION`` only for the one that has a sidecar
          - each ``documentDetails`` entry maps to a ``DocumentIngestResult``
        """
        docs = [
            S3Document(uri="s3://b/a.pdf", metadata_uri="s3://b/a.pdf.metadata.json"),
            S3Document(uri="s3://b/b.pdf"),
        ]
        async with _stubbed() as (ingestor, stubber):
            stubber.add_response(
                "ingest_knowledge_base_documents",
                {
                    "documentDetails": [
                        _detail("s3://b/a.pdf", "STARTING"),
                        _detail("s3://b/b.pdf", "FAILED", "unsupported"),
                    ]
                },
                {
                    "knowledgeBaseId": _KB,
                    "dataSourceId": _DS,
                    "documents": [
                        {
                            "content": _content("s3://b/a.pdf"),
                            "metadata": {
                                "type": "S3_LOCATION",
                                "s3Location": {"uri": "s3://b/a.pdf.metadata.json"},
                            },
                        },
                        {"content": _content("s3://b/b.pdf")},
                    ],
                },
            )
            results = await ingestor.ingest_documents(
                knowledge_base_id=_KB, data_source_id=_DS, documents=docs
            )

        assert results == [
            DocumentIngestResult(uri="s3://b/a.pdf", status="STARTING"),
            DocumentIngestResult(
                uri="s3://b/b.pdf", status="FAILED", status_reason="unsupported"
            ),
        ]

    @staticmethod
    @pytest.mark.asyncio
    async def test_ingest_splits_a_full_batch_into_calls_of_ten() -> None:
        """Test that 25 documents go to Bedrock as calls of 10, 10 and 5.

        **Why this test is important:**
          - The service model caps one ``IngestKnowledgeBaseDocuments`` call at ten
            documents while the interface accepts 25; sending 25 at once is rejected.

        **What it tests:**
          - three calls in order, sized 10, 10 and 5, and every result returned in input
            order
        """
        uris = [f"s3://b/{i:02d}.pdf" for i in range(MAX_DIRECT_DOCUMENTS)]
        async with _stubbed() as (ingestor, stubber):
            for start in range(0, len(uris), _MAX_PER_CALL):
                chunk = uris[start : start + _MAX_PER_CALL]
                stubber.add_response(
                    "ingest_knowledge_base_documents",
                    {"documentDetails": [_detail(u, "STARTING") for u in chunk]},
                    {
                        "knowledgeBaseId": _KB,
                        "dataSourceId": _DS,
                        "documents": [{"content": _content(u)} for u in chunk],
                    },
                )
            results = await ingestor.ingest_documents(
                knowledge_base_id=_KB,
                data_source_id=_DS,
                documents=[S3Document(uri=u) for u in uris],
            )

        assert [r.uri for r in results] == uris

    @staticmethod
    @pytest.mark.asyncio
    async def test_delete_sends_s3_identifiers_and_maps_results() -> None:
        """Test that a delete names each document by its S3 identifier.

        **Why this test is important:**
          - A delete that names the wrong identifier leaves the document's vectors in
            the index, so a removed file stays answerable.

        **What it tests:**
          - the request carries ``documentIdentifiers`` of ``dataSourceType`` ``S3``
          - the response's ``documentDetails`` map to results
        """
        async with _stubbed() as (ingestor, stubber):
            stubber.add_response(
                "delete_knowledge_base_documents",
                {"documentDetails": [_detail("s3://b/a.pdf", "DELETING")]},
                {
                    "knowledgeBaseId": _KB,
                    "dataSourceId": _DS,
                    "documentIdentifiers": [
                        {"dataSourceType": "S3", "s3": {"uri": "s3://b/a.pdf"}}
                    ],
                },
            )
            results = await ingestor.delete_documents(
                knowledge_base_id=_KB, data_source_id=_DS, uris=["s3://b/a.pdf"]
            )

        assert results == [DocumentIngestResult(uri="s3://b/a.pdf", status="DELETING")]

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_throttled_call_is_a_transient_coded_error() -> None:
        """Test that Bedrock throttling an ingest or delete raises a transient error.

        **Why this test is important:**
          - Direct calls share the account's request rate; the retry decorator backs off
            only on a transient code, and a raw SDK error must not cross the boundary.

        **What it tests:**
          - ``ThrottlingException`` from either call raises an ``AppError`` whose code is
            transient
        """
        async with _stubbed() as (ingestor, stubber):
            for op in (
                "ingest_knowledge_base_documents",
                "delete_knowledge_base_documents",
            ):
                stubber.add_client_error(
                    op, service_error_code="ThrottlingException", http_status_code=429
                )
            with pytest.raises(AppError) as ingest_info:
                _ = await ingestor.ingest_documents(
                    knowledge_base_id=_KB,
                    data_source_id=_DS,
                    documents=[S3Document(uri="s3://b/a.pdf")],
                )
            with pytest.raises(AppError) as delete_info:
                _ = await ingestor.delete_documents(
                    knowledge_base_id=_KB, data_source_id=_DS, uris=["s3://b/a.pdf"]
                )

        assert ingest_info.value.is_transient
        assert delete_info.value.is_transient


class TestDirectBatchLimit:
    """Every backend refuses a batch over ``MAX_DIRECT_DOCUMENTS`` before any I/O."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize("backend", ["stub", "bedrock"])
    async def test_more_than_25_documents_is_invalid_input(backend: str) -> None:
        """Test that 26 documents or URIs raise ``InvalidInputError``.

        **Why this test is important:**
          - The limit is the contract a caller sizes its batches by; a backend that
            accepted more would hide an oversized batch until it reached Bedrock.

        **What it tests:**
          - ingest and delete of 26 items raise ``INVALID_INPUT`` on the stub and on the
            Bedrock backend (whose stubber has no response queued, so no call is made)

        Args:
            backend: Which backend to build.

        """
        too_many = [f"s3://b/{i}.pdf" for i in range(MAX_DIRECT_DOCUMENTS + 1)]

        async def _check(ingestor: KnowledgeBaseIngestor) -> None:
            with pytest.raises(InvalidInputError):
                _ = await ingestor.ingest_documents(
                    knowledge_base_id=_KB,
                    data_source_id=_DS,
                    documents=[S3Document(uri=u) for u in too_many],
                )
            with pytest.raises(InvalidInputError):
                _ = await ingestor.delete_documents(
                    knowledge_base_id=_KB, data_source_id=_DS, uris=too_many
                )

        if backend == "stub":
            await _check(StubKnowledgeBaseIngestor())
            return
        async with _stubbed() as (ingestor, _stubber):
            await _check(ingestor)


class TestStubDirectIngestion:
    """The noop backend answers direct calls without AWS."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_stub_indexes_ingested_and_deletes_deleted_documents() -> None:
        """Test that the stub reports every ingested document INDEXED.

        It reports every deleted one DELETING and records both calls.

        **Why this test is important:**
          - A composition root on stub kinds runs the whole direct-ingestion path with
            zero infrastructure, so the stub must answer in Bedrock's vocabulary.

        **What it tests:**
          - ingest returns one ``INDEXED`` result per document, delete one ``DELETING``
            result per URI, and the stub records each call's URIs
        """
        stub = StubKnowledgeBaseIngestor()
        ingested = await stub.ingest_documents(
            knowledge_base_id=_KB,
            data_source_id=_DS,
            documents=[S3Document(uri="s3://b/a.pdf", metadata_uri="s3://b/a.m.json")],
        )
        deleted = await stub.delete_documents(
            knowledge_base_id=_KB, data_source_id=_DS, uris=["s3://b/z.pdf"]
        )

        assert ingested == [DocumentIngestResult(uri="s3://b/a.pdf", status="INDEXED")]
        assert deleted == [DocumentIngestResult(uri="s3://b/z.pdf", status="DELETING")]
        assert stub.ingested == [(_KB, _DS, ("s3://b/a.pdf",))]
        assert stub.deleted == [(_KB, _DS, ("s3://b/z.pdf",))]


def _inner() -> MagicMock:
    """Return a ``create_autospec(KnowledgeBaseIngestor)`` for the decorator tests.

    Returns:
        The spec-bound mock.

    """
    return cast("MagicMock", create_autospec(KnowledgeBaseIngestor, instance=True))


class TestDirectIngestionDecorators:
    """``RetryingIngestor`` retries direct calls; ``PollingIngestor`` passes them on."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_retrying_ingestor_retries_a_throttled_call_once() -> None:
        """Test that a throttled first ingest and delete are each retried once.

        **Why this test is important:**
          - A burst of direct calls is throttled by Bedrock; without a retry the round
            would mark documents failed on a blip.

        **What it tests:**
          - an inner call that raises a transient ``UNAVAILABLE`` once, then answers,
            returns the answer after exactly two attempts, for ingest and for delete
        """
        inner = _inner()
        ok = [DocumentIngestResult(uri="s3://b/a.pdf", status="STARTING")]
        throttled = AppError(ErrorCode.UNAVAILABLE, "ThrottlingException")
        cast("MagicMock", inner.ingest_documents).side_effect = [throttled, ok]
        cast("MagicMock", inner.delete_documents).side_effect = [throttled, ok]
        ingestor = RetryingIngestor(inner, base_delay=0.0, max_delay=0.0)

        ingested = await ingestor.ingest_documents(
            knowledge_base_id=_KB,
            data_source_id=_DS,
            documents=[S3Document(uri="s3://b/a.pdf")],
        )
        deleted = await ingestor.delete_documents(
            knowledge_base_id=_KB, data_source_id=_DS, uris=["s3://b/a.pdf"]
        )

        assert (ingested, deleted) == (ok, ok)
        assert cast("AsyncMock", inner.ingest_documents).await_count == _TWO_ATTEMPTS
        assert cast("AsyncMock", inner.delete_documents).await_count == _TWO_ATTEMPTS

    @staticmethod
    @pytest.mark.asyncio
    async def test_polling_ingestor_passes_direct_calls_through() -> None:
        """Test that ``PollingIngestor`` hands direct calls to the ingestor it wraps.

        **Why this test is important:**
          - The polling decorator is the outermost layer a caller holds; if it dropped
            the new methods, the composed stack could not ingest directly.

        **What it tests:**
          - ingest and delete through ``PollingIngestor`` return the inner results
        """
        inner = _inner()
        ok = [DocumentIngestResult(uri="s3://b/a.pdf", status="INDEXED")]
        cast("MagicMock", inner.ingest_documents).return_value = ok
        cast("MagicMock", inner.delete_documents).return_value = ok
        stack = PollingIngestor(inner)

        assert (
            await stack.ingest_documents(
                knowledge_base_id=_KB,
                data_source_id=_DS,
                documents=[S3Document(uri="s3://b/a.pdf")],
            )
            == ok
        )
        assert (
            await stack.delete_documents(
                knowledge_base_id=_KB, data_source_id=_DS, uris=["s3://b/a.pdf"]
            )
            == ok
        )
