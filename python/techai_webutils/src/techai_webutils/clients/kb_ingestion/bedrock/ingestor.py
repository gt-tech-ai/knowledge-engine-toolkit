"""Real Bedrock Knowledge Base ingestor (bedrock-agent StartIngestionJob/GetIngestionJob).

Thin aiobotocore wrapper used in stage/prod (dev uses the no-op stub). Carved out of unit
coverage (its lines are the live AWS calls) and exercised against real Bedrock in staging.
The pure payload→``IngestionJob`` mapping lives in ``mapping.py`` (unit-tested and
coverage-counted).
"""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Protocol, Self, TypedDict, Unpack, cast, final, override

import aiobotocore.session
from botocore.exceptions import ClientError

from techai_webutils.clients.kb_ingestion.mapping import (
    cohort_batches,
    job_from_payload,
    kb_document_from_payload,
    start_client_token,
    start_error,
)
from techai_webutils.core.errors import AppError, ConflictError
from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    KnowledgeBaseDocument,
    KnowledgeBaseIngestor,
)
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Collection
    from typing import NotRequired

COHORT_REQUEST_INTERVAL_SECONDS = 0.2
"""The least time between two cohort lookups: at most 5 requests per second."""


class _StartIngestionJobRequest(TypedDict):
    """The keyword arguments of the AWS ``start_ingestion_job`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    dataSourceId: str
    """The AWS ``dataSourceId`` request field."""

    clientToken: NotRequired[str]
    """The AWS ``clientToken`` request field."""


class _GetIngestionJobRequest(TypedDict):
    """The keyword arguments of the AWS ``get_ingestion_job`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    dataSourceId: str
    """The AWS ``dataSourceId`` request field."""

    ingestionJobId: str
    """The AWS ``ingestionJobId`` request field."""


class _StopIngestionJobRequest(TypedDict):
    """The keyword arguments of the AWS ``stop_ingestion_job`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    dataSourceId: str
    """The AWS ``dataSourceId`` request field."""

    ingestionJobId: str
    """The AWS ``ingestionJobId`` request field."""


class _ListKnowledgeBaseDocumentsRequest(TypedDict):
    """The keyword arguments of the AWS ``list_knowledge_base_documents`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    dataSourceId: str
    """The AWS ``dataSourceId`` request field."""

    nextToken: NotRequired[str]
    """The AWS ``nextToken`` request field."""


class _GetKnowledgeBaseDocumentsRequest(TypedDict):
    """The keyword arguments of the AWS ``get_knowledge_base_documents`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    dataSourceId: str
    """The AWS ``dataSourceId`` request field."""

    documentIdentifiers: list[dict[str, object]]
    """The AWS ``documentIdentifiers`` request field."""


class _BedrockAgentClient(Protocol):
    """The bedrock-agent client slice this ingestor calls.

    aiobotocore's ``create_client`` has no typed overload for ``"bedrock-agent"`` (it
    resolves to ``Never``), so the yielded client is cast to this Protocol to describe the
    two calls we make.
    """

    async def start_ingestion_job(
        self, **kwargs: Unpack[_StartIngestionJobRequest]
    ) -> dict[str, object]:
        """Start an ingestion job; returns a payload carrying ``ingestionJob``.

        Args:
            **kwargs: The ``StartIngestionJob`` request fields.

        Returns:
            The response, carrying ``ingestionJob``.

        """
        ...

    async def get_ingestion_job(
        self, **kwargs: Unpack[_GetIngestionJobRequest]
    ) -> dict[str, object]:
        """Fetch an ingestion job's state; returns a payload carrying ``ingestionJob``.

        Args:
            **kwargs: The ``GetIngestionJob`` request fields.

        Returns:
            The response, carrying ``ingestionJob``.

        """
        ...

    async def stop_ingestion_job(
        self, **kwargs: Unpack[_StopIngestionJobRequest]
    ) -> dict[str, object]:
        """Request a stop of an ingestion job.

        Returns a payload carrying the STOPPING ``ingestionJob``.

        Args:
            **kwargs: The ``StopIngestionJob`` request fields.

        Returns:
            The response, carrying ``ingestionJob``.

        """
        ...

    async def list_knowledge_base_documents(
        self, **kwargs: Unpack[_ListKnowledgeBaseDocumentsRequest]
    ) -> dict[str, object]:
        """List a data source's documents (one page).

        Returns ``documentDetails`` + ``nextToken``.

        Args:
            **kwargs: The ``ListKnowledgeBaseDocuments`` request fields.

        Returns:
            One page, carrying ``documentDetails`` and ``nextToken``.

        """
        ...

    async def get_knowledge_base_documents(
        self, **kwargs: Unpack[_GetKnowledgeBaseDocumentsRequest]
    ) -> dict[str, object]:
        """Look up to ten documents by identifier; returns ``documentDetails``.

        Args:
            **kwargs: The ``GetKnowledgeBaseDocuments`` request fields.

        Returns:
            The response, carrying ``documentDetails``.

        """
        ...


@final
class BedrockKnowledgeBaseIngestor(KnowledgeBaseIngestor):
    """KnowledgeBaseIngestor backed by AWS Bedrock Agent ingestion jobs.

    A single bedrock-agent client is opened lazily on first use and reused across every
    call — the poll loop can issue hundreds of ``get_ingestion_job`` calls per document,
    so opening a client (credential resolution + connection setup) per call would be
    needlessly expensive. Call ``aclose`` on worker shutdown to release the shared client.
    """

    def __init__(
        self,
        *,
        region: str,
        endpoint: str | None = None,
        cohort_request_interval_seconds: float = COHORT_REQUEST_INTERVAL_SECONDS,
    ) -> None:
        """Store the AWS region + optional endpoint.

        The client is opened lazily on first use.

        ``cohort_request_interval_seconds`` paces ``iter_cohort``'s lookups (5 per
        second).

        Args:
            region: The AWS region.
            endpoint: An endpoint override; ``None`` uses the region's.
            cohort_request_interval_seconds: The pause between
                ``GetKnowledgeBaseDocuments`` batches.

        """
        self._region = region
        self._cohort_interval = cohort_request_interval_seconds
        self._endpoint = endpoint
        self._session = aiobotocore.session.get_session()
        # The exit stack that owns the opened client's async context (entered on first
        # use).
        self._stack: AsyncExitStack | None = None
        self._client: _BedrockAgentClient | None = None

    async def _agent_client(self) -> _BedrockAgentClient:
        """Return the shared bedrock-agent client, opening.

        (and caching) it on first use.

        Returns:
            The shared bedrock-agent client, opening.

        """
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            client_cm = self._session.create_client(
                "bedrock-agent",
                region_name=self._region,
                endpoint_url=self._endpoint,
            )
            self._client = cast(
                "_BedrockAgentClient", await stack.enter_async_context(client_cm)
            )
        return self._client

    async def aclose(self) -> None:
        """Close the shared bedrock-agent client if one was opened (idempotent)."""
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._client = None

    @override
    async def __aenter__(self) -> Self:
        """Enter the async context; the shared client opens lazily on first call.

        Returns:
            This instance.

        """
        return self

    @override
    async def __aexit__(self, *exc: object) -> None:
        """Release the shared client on context exit.

        Args:
            *exc: The exception triple, if any.

        """
        await self.aclose()

    @override
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, round_id: str = ""
    ) -> IngestionJob:
        """Start a Bedrock ingestion job for the KB data source.

        A botocore ``ConflictException`` — Bedrock rejecting a second start while a job is
        already STARTING/IN_PROGRESS/STOPPING for this data source (Bedrock serializes
        ingestion) — is mapped to ``core/errors.ConflictError`` so the caller handles it
        as "a job is already running" (skip this round / reattach) rather than a raw SDK
        error that escapes as an ERROR-level log loop. ``ServiceQuotaExceededException``
        and ``ThrottlingException`` become ``UnavailableError`` (transient), so the retry
        stack backs off. Any other failure is coded by the shared botocore boundary
        (``ValidationException`` stays permanent). A non-empty ``round_id`` sends a
        ``clientToken`` derived from the knowledge base, data source and round, so a
        retried start is idempotent.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            round_id: The sync round, so a retried start attaches to the job it already
                started; empty starts a new job.

        Returns:
            A Bedrock ingestion job for the KB data source.

        Raises:
            _start_failure: The coded error for a refused start.

        """
        client = await self._agent_client()
        kwargs: _StartIngestionJobRequest = {
            "knowledgeBaseId": knowledge_base_id,
            "dataSourceId": data_source_id,
        }
        token = start_client_token(knowledge_base_id, data_source_id, round_id)
        if token is not None:
            kwargs["clientToken"] = token
        try:
            resp = await client.start_ingestion_job(**kwargs)
        except ClientError as exc:
            raise _start_failure(exc, data_source_id) from exc
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    @override
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Fetch the current state of a Bedrock ingestion job.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to read.

        Returns:
            The current state of a Bedrock ingestion job.

        """
        client = await self._agent_client()
        resp = await client.get_ingestion_job(
            knowledgeBaseId=knowledge_base_id,
            dataSourceId=data_source_id,
            ingestionJobId=job_id,
        )
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    @override
    async def stop_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, job_id: str
    ) -> IngestionJob:
        """Request a stop of a Bedrock ingestion job (the recovery for a stuck job).

        A ``ConflictException`` — the job already reached a terminal state before the stop
        landed — is mapped to ``core/errors.ConflictError`` (consistent with ``start``) so
        the caller treats it as "already stopped/finished" rather than a raw SDK error;
        any other failure is coded by the shared botocore boundary.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to stop.

        Returns:
            The job after the stop request.

        Raises:
            botocore_error_to_app_error: The coded error for any other failure.
            ConflictError: Bedrock ingestion job ``job_id`` already terminal; nothing to
                stop.

        """
        client = await self._agent_client()
        try:
            resp = await client.stop_ingestion_job(
                knowledgeBaseId=knowledge_base_id,
                dataSourceId=data_source_id,
                ingestionJobId=job_id,
            )
        except ClientError as exc:
            if exc.response.get("Error", {}).get("Code") == "ConflictException":
                msg = f"bedrock ingestion job {job_id} already terminal; nothing to stop"
                raise ConflictError(msg) from exc
            raise botocore_error_to_app_error(exc, "bedrock stop ingestion job") from exc
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    @override
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List every document in the KB data source.

        Following ``nextToken`` across all pages.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.

        Returns:
            Every document in the KB data source.

        """
        client = await self._agent_client()
        docs: list[KnowledgeBaseDocument] = []
        next_token: str | None = None
        while True:
            kwargs: _ListKnowledgeBaseDocumentsRequest = {
                "knowledgeBaseId": knowledge_base_id,
                "dataSourceId": data_source_id,
            }
            if next_token:
                kwargs["nextToken"] = next_token
            resp = await client.list_knowledge_base_documents(**kwargs)
            docs.extend(
                kb_document_from_payload(detail)
                for detail in cast(
                    "list[dict[str, object]]", resp.get("documentDetails", [])
                )
            )
            next_token = cast("str | None", resp.get("nextToken"))
            if not next_token:
                break
        return docs

    @override
    async def iter_cohort(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        s3_uris: Collection[str],
    ) -> AsyncIterator[KnowledgeBaseDocument]:
        """Look the cohort's documents up by URI with ``GetKnowledgeBaseDocuments``.

        Ten per call.

        Calls are at least ``cohort_request_interval_seconds`` apart, and the lookup ends
        once every URI has been asked for, instead of paging the whole data source.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            s3_uris: The source objects the sync round started with.

        Yields:
            Each document in the cohort.

        """
        client = await self._agent_client()
        for n, batch in enumerate(cohort_batches(sorted(set(s3_uris)))):
            if n:
                await asyncio.sleep(self._cohort_interval)
            resp = await client.get_knowledge_base_documents(
                knowledgeBaseId=knowledge_base_id,
                dataSourceId=data_source_id,
                documentIdentifiers=batch,
            )
            for detail in cast(
                "list[dict[str, object]]", resp.get("documentDetails", [])
            ):
                yield kb_document_from_payload(detail)


def _start_failure(exc: ClientError, data_source_id: str) -> AppError:
    """Code a failed ``StartIngestionJob`` call.

    Args:
        exc: The SDK's error.
        data_source_id: The data source the start was for.

    Returns:
        ``start_error``'s mapping of the code, else the shared botocore boundary's.

    """
    mapped = start_error(
        str(exc.response.get("Error", {}).get("Code", "")), data_source_id
    )
    if mapped is not None:
        return mapped
    return botocore_error_to_app_error(exc, "bedrock start ingestion job")
