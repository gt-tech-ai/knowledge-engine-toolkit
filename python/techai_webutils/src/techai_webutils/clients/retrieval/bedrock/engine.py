"""Bedrock Knowledge Base retrieval engine (bedrock-agent-runtime Retrieve).

Thin aiobotocore wrapper. The consumer may inject a ``filter_builder`` (the KB metadata filter pushed
down with each query; none by default) and a ``document_id_resolver`` (how a passage's document id is
derived from its chunk metadata; by default the ``document_id`` key, else the chunk's source URI).
Passages are re-validated server-side by ``FilteringRetrievalEngine`` (defense-in-depth). Botocore
failures surface as coded ``AppError``s (``foundation/resilience/aws_boundary``). Carved out of unit
coverage; exercised against a real Knowledge Base.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from contextlib import AsyncExitStack
from typing import Protocol, Self, cast

import aiobotocore.session  # type: ignore[import-untyped]
from botocore.exceptions import BotoCoreError, ClientError

from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error

FilterBuilder = Callable[[Mapping[str, str]], dict[str, object] | None]
"""Builds the KB metadata filter from the request filters; ``None`` pushes no filter down."""

DocumentIdResolver = Callable[[Mapping[str, str], Mapping[str, str]], str]
"""Resolves a passage's document id from ``(chunk metadata, request filters)``; ``""`` when unknown."""

SOURCE_URI_KEY = "x-amz-bedrock-kb-source-uri"
"""The chunk-metadata key Bedrock KB Retrieve results carry with the source document's URI."""


class _BedrockAgentRuntimeClient(Protocol):
    """The bedrock-agent-runtime client slice this engine calls (aiobotocore is untyped for it).

    ``create_client("bedrock-agent-runtime")`` has no typed overload (it resolves to ``Never``), so
    the yielded client is cast to this Protocol to describe the single ``retrieve`` call we make.
    """

    async def retrieve(
        self,
        *,
        knowledgeBaseId: str,
        retrievalQuery: dict[str, str],
        retrievalConfiguration: dict[str, object],
    ) -> dict[str, object]:
        """Retrieve passages; returns a payload carrying ``retrievalResults``."""
        ...


class BedrockRetrievalEngine(RetrievalEngine):
    """RetrievalEngine backed by AWS Bedrock Knowledge Base ``Retrieve``.

    A single bedrock-agent-runtime client is opened lazily on first use and reused across queries
    (opening one per request would repeat credential resolution + connection setup on the hot query
    path); call ``aclose`` on shutdown to release it.
    """

    def __init__(
        self,
        *,
        region: str,
        knowledge_base_id: str,
        endpoint: str | None = None,
        search_type: str = "semantic",
        reranking_model: str = "",
        filter_builder: FilterBuilder | None = None,
        document_id_resolver: DocumentIdResolver | None = None,
    ) -> None:
        """Store the region, KB id, endpoint, and search strategy; the client opens lazily on first use.

        ``search_type`` selects semantic (vector-only) vs "hybrid" (vector+keyword, Retrieve's
        overrideSearchType); ``reranking_model`` is a Cohere reranker model id (e.g.
        ``cohere.rerank-v3-5:0``) whose presence enables Retrieve's rerankingConfiguration — both are
        config-selected. Empty ``reranking_model`` = no reranking. ``filter_builder`` builds the KB
        metadata filter (default: none) and ``document_id_resolver`` resolves each passage's document
        id (default: ``metadata_document_id`` — the chunk's ``document_id`` metadata, else its source
        URI), so a consumer's metadata keys and object-key layout are its own.
        """
        self._region = region
        self._kb_id = knowledge_base_id
        self._endpoint = endpoint
        self._search_type = search_type
        self._reranking_model = reranking_model
        self._build_filter = filter_builder
        self._resolve_document_id = document_id_resolver or metadata_document_id
        self._session = aiobotocore.session.get_session()
        # The exit stack that owns the opened client's async context (entered on first use).
        self._stack: AsyncExitStack | None = None
        self._client: _BedrockAgentRuntimeClient | None = None

    async def _runtime_client(self) -> _BedrockAgentRuntimeClient:
        """Return the shared bedrock-agent-runtime client, opening (and caching) it on first use."""
        if self._client is None:
            self._stack = stack = AsyncExitStack()
            client_cm = self._session.create_client(
                "bedrock-agent-runtime",
                region_name=self._region,
                endpoint_url=self._endpoint,
            )
            self._client = cast(
                "_BedrockAgentRuntimeClient", await stack.enter_async_context(client_cm)
            )
        return self._client

    async def aclose(self) -> None:
        """Close the shared client if one was opened (idempotent)."""
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

    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve passages from the Bedrock Knowledge Base.

        ``index_id`` is the knowledge base id for this call (e.g. per-tenant routing); only ``None``
        falls back to the construction-time KB. An empty per-call id, or ``None`` with no default,
        raises an ``InternalError`` rather than silently querying an unintended KB — a router that
        returns "" for an unmapped tenant, or a missed upstream routing check, errors here instead of
        leaking the shared or another tenant's KB (defense-in-depth).

        Builds the Retrieve request from the config-selected strategy: numberOfResults (the wide pool),
        the consumer's metadata filter when a ``filter_builder`` returns one, overrideSearchType for
        hybrid search, and a Cohere rerankingConfiguration when a reranker is configured. A botocore
        failure raises a coded ``AppError`` (throttling / 5xx → transient ``UNAVAILABLE``).
        """
        if index_id is not None and not index_id:
            msg = (
                "empty per-call knowledge base id (pass None to use the engine's default)"
            )
            raise InternalError(msg)
        kb_id = index_id if index_id is not None else self._kb_id
        if not kb_id:
            msg = "no knowledge base id: no per-call id and the engine has no configured default"
            raise InternalError(msg)
        client = await self._runtime_client()
        request = filters or {}
        vector_config: dict[str, object] = {"numberOfResults": top_k}
        if (
            self._build_filter is not None
            and (kb_filter := self._build_filter(request)) is not None
        ):
            vector_config["filter"] = kb_filter
        if self._search_type == "hybrid":
            vector_config["overrideSearchType"] = "HYBRID"
        if self._reranking_model:
            vector_config["rerankingConfiguration"] = {
                "type": "BEDROCK_RERANKING_MODEL",
                "bedrockRerankingConfiguration": {
                    "modelConfiguration": {
                        "modelArn": (
                            f"arn:aws:bedrock:{self._region}::foundation-model/{self._reranking_model}"
                        ),
                    },
                },
            }
        try:
            response = await client.retrieve(
                knowledgeBaseId=kb_id,
                retrievalQuery={"text": query},
                retrievalConfiguration={"vectorSearchConfiguration": vector_config},
            )
        except (ClientError, BotoCoreError) as exc:
            raise botocore_error_to_app_error(exc, "bedrock retrieve") from exc
        results = cast("list[dict[str, object]]", response.get("retrievalResults", []))
        return [_to_result(item, request, self._resolve_document_id) for item in results]


def _to_float(value: object) -> float:
    """Coerce a JSON number to float, defaulting to 0.0."""
    return float(value) if isinstance(value, (int, float)) else 0.0


def metadata_document_id(metadata: Mapping[str, str], filters: Mapping[str, str]) -> str:  # noqa: ARG001
    """Return the chunk's ``document_id`` metadata, else its source URI, else ``""`` (the default resolver).

    Standard Bedrock KB chunks carry no ``document_id``, but every Retrieve result carries the source
    document's URI (``x-amz-bedrock-kb-source-uri``) — a stable per-document id that assumes nothing
    about the consumer's object-key layout.
    """
    return metadata.get("document_id") or metadata.get(SOURCE_URI_KEY, "")


def _to_result(
    item: dict[str, object],
    filters: Mapping[str, str],
    resolve_document_id: DocumentIdResolver,
) -> RetrievalResult:
    """Map one Bedrock retrievalResult to a RetrievalResult.

    The chunk metadata is carried as-is, so a passage missing a scope key stays missing it and a
    ``MetadataEquals`` policy drops it (fail closed) instead of trusting the request for it.
    """
    metadata_raw = item.get("metadata", {})
    metadata = (
        {str(k): str(v) for k, v in cast("dict[object, object]", metadata_raw).items()}
        if isinstance(metadata_raw, dict)
        else {}
    )
    content = item.get("content", {})
    text = (
        cast("dict[str, object]", content).get("text", "")
        if isinstance(content, dict)
        else ""
    )
    page = metadata.get("page_number")
    return RetrievalResult(
        document_id=resolve_document_id(metadata, filters),
        document_name=metadata.get("document_name", ""),
        chunk_content=str(text),
        score=_to_float(item.get("score", 0.0)),
        page_number=int(page) if page is not None and page.isdigit() else None,
        metadata=metadata,
    )
