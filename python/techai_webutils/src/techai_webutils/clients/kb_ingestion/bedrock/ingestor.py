"""Real Bedrock Knowledge Base ingestor (bedrock-agent StartIngestionJob/GetIngestionJob).

Thin aiobotocore wrapper used in stage/prod (dev uses the no-op stub). Carved out of unit
coverage (its lines are the live AWS calls) and exercised against real Bedrock in staging.
The pure payload→``IngestionJob`` mapping lives in ``mapping.py`` (unit-tested and
coverage-counted).
"""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, Any, Protocol, Self, cast

import aiobotocore.session  # type: ignore[import-untyped]
from botocore.exceptions import ClientError  # type: ignore[import-untyped]

from techai_webutils.clients.kb_ingestion.mapping import (
    cohort_batches,
    job_from_payload,
    kb_document_from_payload,
    start_client_token,
    start_error,
)
from techai_webutils.core.errors import ConflictError
from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    KnowledgeBaseDocument,
    KnowledgeBaseIngestor,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Collection

COHORT_REQUEST_INTERVAL_SECONDS = 0.2
"""The least time between two cohort lookups: at most 5 requests per second."""


class _BedrockAgentClient(Protocol):
    """The bedrock-agent client slice this ingestor calls.

    aiobotocore's ``create_client`` has no typed overload for ``"bedrock-agent"`` (it
    resolves to ``Never``), so the yielded client is cast to this Protocol to describe the
    two calls we make.
    """

    async def start_ingestion_job(
        self,
        *,
        knowledgeBaseId: str,
        dataSourceId: str,
        clientToken: str = ...,
    ) -> dict[str, object]:
        """Start an ingestion job; returns a payload carrying ``ingestionJob``."""
        ...

    async def get_ingestion_job(
        self,
        *,
        knowledgeBaseId: str,
        dataSourceId: str,
        ingestionJobId: str,
    ) -> dict[str, object]:
        """Fetch an ingestion job's state; returns a payload carrying ``ingestionJob``."""
        ...

    async def stop_ingestion_job(
        self,
        *,
        knowledgeBaseId: str,
        dataSourceId: str,
        ingestionJobId: str,
    ) -> dict[str, object]:
        """Request a stop of an ingestion job.

        Returns a payload carrying the STOPPING ``ingestionJob``.
        """
        ...

    async def list_knowledge_base_documents(
        self,
        *,
        knowledgeBaseId: str,
        dataSourceId: str,
        nextToken: str = ...,
    ) -> dict[str, object]:
        """List a data source's documents (one page).

        Returns ``documentDetails`` + ``nextToken``.
        """
        ...

    async def get_knowledge_base_documents(
        self,
        *,
        knowledgeBaseId: str,
        dataSourceId: str,
        documentIdentifiers: list[dict[str, object]],
    ) -> dict[str, object]:
        """Look up to ten documents by identifier; returns ``documentDetails``."""
        ...


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

    async def __aenter__(self) -> Self:
        """Enter the async context; the shared client opens lazily on first call."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Release the shared client on context exit."""
        await self.aclose()

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
        stack backs off. Other ``ClientError``s (``ValidationException`` included)
        propagate unchanged. A non-empty ``round_id`` sends a ``clientToken`` derived from
        the knowledge base, data source and round, so a retried start is idempotent.
        """
        client = await self._agent_client()
        kwargs: dict[str, str] = {
            "knowledgeBaseId": knowledge_base_id,
            "dataSourceId": data_source_id,
        }
        token = start_client_token(knowledge_base_id, data_source_id, round_id)
        if token is not None:
            kwargs["clientToken"] = token
        try:
            resp = await client.start_ingestion_job(**cast("Any", kwargs))
        except ClientError as exc:
            mapped = start_error(
                str(exc.response.get("Error", {}).get("Code", "")), data_source_id
            )
            if mapped is not None:
                raise mapped from exc
            raise
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Fetch the current state of a Bedrock ingestion job."""
        client = await self._agent_client()
        resp = await client.get_ingestion_job(
            knowledgeBaseId=knowledge_base_id,
            dataSourceId=data_source_id,
            ingestionJobId=job_id,
        )
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    async def stop_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, job_id: str
    ) -> IngestionJob:
        """Request a stop of a Bedrock ingestion job (the recovery for a stuck job).

        A ``ConflictException`` — the job already reached a terminal state before the stop
        landed — is mapped to ``core/errors.ConflictError`` (consistent with ``start``) so
        the caller treats it as "already stopped/finished" rather than a raw SDK error;
        other ``ClientError``s propagate.
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
            raise
        return job_from_payload(cast("dict[str, object]", resp["ingestionJob"]))

    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List every document in the KB data source.

        Following ``nextToken`` across all pages.
        """
        client = await self._agent_client()
        docs: list[KnowledgeBaseDocument] = []
        next_token: str | None = None
        while True:
            kwargs: dict[str, object] = {
                "knowledgeBaseId": knowledge_base_id,
                "dataSourceId": data_source_id,
            }
            if next_token:
                kwargs["nextToken"] = next_token
            resp = await client.list_knowledge_base_documents(**cast("Any", kwargs))
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
