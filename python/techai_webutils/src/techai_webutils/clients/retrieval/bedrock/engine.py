"""Bedrock Knowledge Base retrieval engine (bedrock-agent-runtime Retrieve).

Thin aiobotocore wrapper. The consumer may inject a ``filter_builder`` (the KB metadata
filter pushed down with each query; none by default) and a ``document_id_resolver`` (how a
passage's document id is derived from its chunk metadata; by default the ``document_id``
key, else the chunk's source URI). Passages are re-validated server-side by
``FilteringRetrievalEngine`` (defense-in-depth). Botocore failures surface as coded
``AppError``s (``foundation/resilience/aws_boundary``). Carved out of unit coverage;
exercised against a real Knowledge Base.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from contextlib import AsyncExitStack
from dataclasses import dataclass
from typing import Protocol, Self, TypedDict, Unpack, cast, final, override

import aiobotocore.session
from botocore.exceptions import BotoCoreError, ClientError

from techai_webutils.clients.retrieval.bedrock.filters import (
    FilterBuilder,
    bedrock_filter,
)
from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.retrieval import (
    FilterExpr,
    RetrievalEngine,
    RetrievalResult,
)
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error

DocumentIdResolver = Callable[[Mapping[str, str], Mapping[str, str]], str]
"""Resolves a passage's document id from ``(chunk metadata, request filters)``; ``""``
when unknown."""

SOURCE_URI_KEY = "x-amz-bedrock-kb-source-uri"
"""The chunk-metadata key Bedrock KB Retrieve results carry with the source document's
URI."""


class _RetrieveRequest(TypedDict):
    """The keyword arguments of the AWS ``retrieve`` call."""

    knowledgeBaseId: str
    """The AWS ``knowledgeBaseId`` request field."""

    retrievalQuery: dict[str, str]
    """The AWS ``retrievalQuery`` request field."""

    retrievalConfiguration: dict[str, object]
    """The AWS ``retrievalConfiguration`` request field."""


class _BedrockAgentRuntimeClient(Protocol):
    """The bedrock-agent-runtime client slice this engine calls.

    aiobotocore is untyped for it.

    ``create_client("bedrock-agent-runtime")`` has no typed overload (it resolves to
    ``Never``), so the yielded client is cast to this Protocol to describe the single
    ``retrieve`` call we make.
    """

    async def retrieve(self, **kwargs: Unpack[_RetrieveRequest]) -> dict[str, object]:
        """Retrieve passages; returns a payload carrying ``retrievalResults``.

        Args:
            **kwargs: The ``Retrieve`` request fields.

        Returns:
            The response, carrying ``retrievalResults``.

        """
        ...


@dataclass(frozen=True, kw_only=True)
class BedrockRetrievalOptions:
    """What ``BedrockRetrievalEngine`` takes, grouped into one value."""

    region: str
    """The ``region`` value of ``BedrockRetrievalEngine``."""

    knowledge_base_id: str
    """The ``knowledge_base_id`` value of ``BedrockRetrievalEngine``."""

    endpoint: str | None = None
    """The ``endpoint`` value of ``BedrockRetrievalEngine``."""

    search_type: str = "semantic"
    """The ``search_type`` value of ``BedrockRetrievalEngine``."""

    reranking_model: str = ""
    """The ``reranking_model`` value of ``BedrockRetrievalEngine``."""

    filter_builder: FilterBuilder | None = None
    """The ``filter_builder`` value of ``BedrockRetrievalEngine``."""

    document_id_resolver: DocumentIdResolver | None = None
    """The ``document_id_resolver`` value of ``BedrockRetrievalEngine``."""


@final
class BedrockRetrievalEngine(RetrievalEngine):
    """RetrievalEngine backed by AWS Bedrock Knowledge Base ``Retrieve``.

    A single bedrock-agent-runtime client is opened lazily on first use and reused across
    queries (opening one per request would repeat credential resolution + connection setup
    on the hot query path); call ``aclose`` on shutdown to release it.
    """

    def __init__(self, options: BedrockRetrievalOptions) -> None:
        """Store the region, KB id, endpoint and search strategy.

        The client opens lazily on first use.

        ``search_type`` selects semantic (vector-only) vs "hybrid" (vector+keyword,
        Retrieve's overrideSearchType); ``reranking_model`` is a Cohere reranker model id
        (e.g. ``cohere.rerank-v3-5:0``) whose presence enables Retrieve's
        rerankingConfiguration — both are config-selected. Empty ``reranking_model`` = no
        reranking. ``filter_builder`` builds the KB metadata filter (default: none) and
        ``document_id_resolver`` resolves each passage's document id (default:
        ``metadata_document_id`` — the chunk's ``document_id`` metadata, else its source
        URI), so a consumer's metadata keys and object-key layout are its own.

        Args:
            options: The region, knowledge base, endpoint, search strategy and seams.

        """
        self._region = options.region
        self._kb_id = options.knowledge_base_id
        self._endpoint = options.endpoint
        self._search_type = options.search_type
        self._reranking_model = options.reranking_model
        self._build_filter = options.filter_builder
        self._resolve_document_id = options.document_id_resolver or metadata_document_id
        self._session = aiobotocore.session.get_session()
        # The exit stack that owns the opened client's async context (entered on first
        # use).
        self._stack: AsyncExitStack | None = None
        self._client: _BedrockAgentRuntimeClient | None = None

    async def _runtime_client(self) -> _BedrockAgentRuntimeClient:
        """Return the shared bedrock-agent-runtime client.

        It is opened and cached on first use.

        Returns:
            The shared bedrock-agent-runtime client, opening.

        """
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
    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        where: FilterExpr | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve passages from the Bedrock Knowledge Base.

        ``index_id`` is the knowledge base id for this call (e.g. per-tenant routing);
        only ``None`` falls back to the construction-time KB. An empty per-call id, or
        ``None`` with no default, raises an ``InternalError`` rather than silently
        querying an unintended KB — a router that returns "" for an unmapped tenant, or a
        missed upstream routing check, errors here instead of leaking the shared or
        another tenant's KB (defense-in-depth).

        Builds the Retrieve request from the config-selected strategy: numberOfResults
        (the wide pool), the consumer's metadata filter when a ``filter_builder`` returns
        one ANDed with ``where``, overrideSearchType for hybrid search, and a Cohere
        rerankingConfiguration when a reranker is configured. A botocore failure raises a
        coded ``AppError`` (throttling / 5xx → transient ``UNAVAILABLE``).

        Args:
            query: The search query.
            top_k: The most chunks to return.
            filters: Metadata every returned chunk must match; ``None`` matches all.
            where: A further filter ANDed with ``filters``; ``None`` adds none.
            index_id: The index to search; ``None`` searches the engine's default.

        Returns:
            The retrieved passages.

        Raises:
            botocore_error_to_app_error: Bedrock retrieve.

        """
        kb_id = self._knowledge_base_id(index_id)
        client = await self._runtime_client()
        request = filters or {}
        try:
            response = await client.retrieve(
                knowledgeBaseId=kb_id,
                retrievalQuery={"text": query},
                retrievalConfiguration={
                    "vectorSearchConfiguration": self._vector_search_config(
                        top_k, request, where
                    )
                },
            )
        except (ClientError, BotoCoreError) as exc:
            raise botocore_error_to_app_error(exc, "bedrock retrieve") from exc
        results = cast("list[dict[str, object]]", response.get("retrievalResults", []))
        return [_to_result(item, request, self._resolve_document_id) for item in results]

    def _knowledge_base_id(self, index_id: str | None) -> str:
        """Return the knowledge base to query.

        The per-call id, else the configured default.

        Args:
            index_id: The index the caller named; ``None`` uses the configured one.

        Returns:
            The knowledge base to query: the per-call id.

        Raises:
            InternalError: The per-call id is empty, or there is neither a per-call id nor
                a default.

        """
        if index_id is not None and not index_id:
            msg = (
                "empty per-call knowledge base id (pass None to use the engine's default)"
            )
            raise InternalError(msg)
        kb_id = index_id if index_id is not None else self._kb_id
        if not kb_id:
            msg = (
                "no knowledge base id: no per-call id and the engine has no configured "
                "default"
            )
            raise InternalError(msg)
        return kb_id

    def _vector_search_config(
        self, top_k: int, request: Mapping[str, str], where: FilterExpr | None
    ) -> dict[str, object]:
        """Build Retrieve's ``vectorSearchConfiguration``.

        Pool size, filter, search type and reranker. The filter is the builder's and
        ``where``'s, ``andAll`` when both are present.

        Args:
            top_k: The most passages to return.
            request: The request's filter fields.
            where: The call's further filter; ``None`` adds none.

        Returns:
            The ``vectorSearchConfiguration`` value.

        """
        vector_config: dict[str, object] = {"numberOfResults": top_k}
        built = None if self._build_filter is None else self._build_filter(request)
        narrowing = None if where is None else bedrock_filter(where)
        clauses = [f for f in (built, narrowing) if f is not None]
        if clauses:
            vector_config["filter"] = (
                clauses[0] if len(clauses) == 1 else {"andAll": clauses}
            )
        if self._search_type == "hybrid":
            vector_config["overrideSearchType"] = "HYBRID"
        if self._reranking_model:
            model_arn = (
                f"arn:aws:bedrock:{self._region}::"
                f"foundation-model/{self._reranking_model}"
            )
            vector_config["rerankingConfiguration"] = {
                "type": "BEDROCK_RERANKING_MODEL",
                "bedrockRerankingConfiguration": {
                    "modelConfiguration": {"modelArn": model_arn},
                },
            }
        return vector_config


def _to_float(value: object) -> float:
    """Coerce a JSON number to float, defaulting to 0.0.

    Args:
        value: The JSON value.

    Returns:
        The float, or ``0.0``.

    """
    return float(value) if isinstance(value, (int, float)) else 0.0


def metadata_document_id(metadata: Mapping[str, str], filters: Mapping[str, str]) -> str:
    """Return the chunk's ``document_id`` metadata, else its source URI.

    Otherwise ``""``. This is the default resolver.

    Standard Bedrock KB chunks carry no ``document_id``, but every Retrieve result carries
    the source document's URI (``x-amz-bedrock-kb-source-uri``) — a stable per-document id
    that assumes nothing about the consumer's object-key layout.

    Args:
        metadata: The passage's metadata.
        filters: The request's filter fields.

    Returns:
        The chunk's ``document_id`` metadata, else its source URI, else ``""``.

    """
    del filters
    return metadata.get("document_id") or metadata.get(SOURCE_URI_KEY, "")


def _to_result(
    item: dict[str, object],
    filters: Mapping[str, str],
    resolve_document_id: DocumentIdResolver,
) -> RetrievalResult:
    """Map one Bedrock retrievalResult to a RetrievalResult.

    The chunk metadata is carried as-is, so a passage missing a scope key stays missing it
    and a ``MetadataEquals`` policy drops it (fail closed) instead of trusting the request
    for it.

    Args:
        item: One ``retrievalResults`` entry.
        filters: The request's filter fields.
        resolve_document_id: Names the passage's document.

    Returns:
        The mapped result.

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
