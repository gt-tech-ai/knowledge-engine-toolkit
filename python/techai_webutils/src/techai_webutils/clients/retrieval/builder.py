"""Config-selected retrieval engine factory (foundation/logger ``NewFromConfig`` pattern).

``RetrievalConfig.kind`` selects the engine (a stub, a local vector store, or a Bedrock Knowledge
Base). The selected engine is always wrapped in ``FilteringRetrievalEngine`` with the config's score
floor plus the consumer's scope policies, so server-side re-validation applies in every environment.
Unknown kinds fail loudly. Heavy backends are imported lazily, so the stub path never loads the AWS
SDK or the vector clients.

Push-down is explicit: a ``MetadataEquals`` scope reaches the backend only through that kind's seam
(``filter_builder`` for Bedrock, ``store_filter_keys`` for the vector store); otherwise the backend
returns the index-wide top_k and the policy post-filters it. The factory warns when a scope is not
pushed down, and logs (at info) a supplied seam the selected kind does not use.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
)
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine
from techai_webutils.core.errors import AppTypeError, AppValueError
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.clients.retrieval.bedrock.engine import (
        DocumentIdResolver,
        FilterBuilder,
    )
    from techai_webutils.clients.retrieval.filtering import PassagePolicy
    from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult

logger = get_logger(__name__)


class RetrievalKind(StrEnum):
    """Which retrieval engine implementation to build."""

    STUB = "stub"
    """The in-memory engine over a consumer-supplied corpus (local development and tests)."""
    BEDROCK = "bedrock"
    """The AWS Bedrock Knowledge Base retrieval engine."""
    QDRANT = "qdrant"
    """A Qdrant vector store fed by an Ollama embedder (local vector development)."""


@dataclass(frozen=True, slots=True)
class RetrievalConfig:
    """Retrieval engine configuration (the consumer maps it from its own config section)."""

    kind: RetrievalKind = RetrievalKind.STUB
    """Selects the engine implementation."""
    region: str = "us-east-1"
    """AWS region for the Bedrock KB client (unused by the stub)."""
    endpoint: str | None = None
    """Optional Bedrock endpoint override (e.g. a VPC endpoint); ``None`` uses AWS default resolution."""
    knowledge_base_id: str = ""
    """The Bedrock Knowledge Base id to query (empty for the stub)."""
    search_type: str = "semantic"
    """KB search strategy: "semantic" (vector only) | "hybrid" (vector + keyword; the KB's
    OpenSearch storage supports it). "hybrid" sets Retrieve's overrideSearchType."""
    reranking_kind: str = "none"
    """Reranker selection: "none" | "bedrock_rerank" (a Cohere Rerank model reorders the
    candidate pool via Retrieve's rerankingConfiguration)."""
    reranking_model: str = ""
    """Reranker model id for the bedrock_rerank kind, e.g. "cohere.rerank-v3-5:0" (empty → no reranking)."""
    min_score: float = 0.5
    """Relevance floor for the filtering decorator; passages below it are dropped (including a stub's
    hand-written passages). Backends score on different scales — Bedrock Knowledge Base scores run
    lower than a local vector store's, so the 0.5 default can drop strong Bedrock matches — so set it
    per engine kind. ``None`` is rejected (there is no per-kind default)."""
    # Local vector stack (``kind == QDRANT``): a Qdrant store fed by an Ollama embedder; ignored by the
    # stub/bedrock kinds. Hosts default to localhost.
    vector_url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint the dev engine reads/writes points from."""
    vector_collection: str = "documents"
    """Qdrant collection name holding the document embeddings."""
    embedding_host: str = "http://localhost:11434"
    """Ollama host serving the embedding model."""
    embedding_model: str = "nomic-embed-text"
    """Embedding model name requested from Ollama."""
    embedding_dimension: int = 768
    """Vector dimensionality of ``embedding_model`` (must match the Qdrant collection)."""

    def __post_init__(self) -> None:
        """Reject a non-numeric ``min_score`` (e.g. ``None``) here rather than on every retrieve."""
        # Read as ``object``: the annotation says float, but a caller can still pass None.
        min_score = cast("object", self.min_score)
        if isinstance(min_score, bool) or not isinstance(min_score, int | float):
            msg = f"RetrievalConfig.min_score must be a number, got {min_score!r}"
            raise AppTypeError(msg)


def wrap_retrieval_engine(
    inner: RetrievalEngine,
    config: RetrievalConfig,
    *,
    policies: Sequence[PassagePolicy],
) -> RetrievalEngine:
    """Wrap ``inner`` in the filtering the factory applies: ``MinScore(config.min_score)``, then ``policies``.

    ``new_retrieval_engine_from_config`` wraps every kind it builds with this; a consumer with its own
    engine (a custom corpus, a test double of the backend) calls it directly so that engine is scoped
    exactly like the factory's.
    """
    return FilteringRetrievalEngine(inner, [MinScore(config.min_score), *policies])


def new_retrieval_engine_from_config(
    config: RetrievalConfig,
    *,
    policies: Sequence[PassagePolicy],
    filter_builder: FilterBuilder | None = None,
    document_id_resolver: DocumentIdResolver | None = None,
    store_filter_keys: Sequence[str] = (),
    stub_passages: Sequence[RetrievalResult] = (),
) -> RetrievalEngine:
    """Build the retrieval engine selected by ``config.kind``, wrapped by ``wrap_retrieval_engine``.

    The wrapping applies ``MinScore(config.min_score)`` plus ``policies`` — the consumer's scope
    rules (e.g. ``MetadataEquals`` on its tenant key); pass an empty list only when there is no scope
    to enforce. The other seams (code, not config data) reach one kind each: ``filter_builder`` and
    ``document_id_resolver`` the Bedrock engine, ``store_filter_keys`` the vector engine's push-down,
    ``stub_passages`` the stub's corpus. A ``MetadataEquals`` scope with no push-down seam for the
    selected kind is logged as a warning; a seam the selected kind ignores is logged at info.
    """
    _warn_about_seams(
        config.kind,
        policies,
        filter_builder=filter_builder,
        document_id_resolver=document_id_resolver,
        store_filter_keys=store_filter_keys,
        stub_passages=stub_passages,
    )
    inner: RetrievalEngine
    if config.kind is RetrievalKind.STUB:
        inner = StubRetrievalEngine(stub_passages)
    elif config.kind is RetrievalKind.BEDROCK:
        # Lazy import: keep aiobotocore off the dev/stub path (loaded only in stage/prod).
        from techai_webutils.clients.retrieval.bedrock import BedrockRetrievalEngine  # noqa: PLC0415

        inner = BedrockRetrievalEngine(
            region=config.region,
            knowledge_base_id=config.knowledge_base_id,
            endpoint=config.endpoint,
            search_type=config.search_type,
            # The reranker model is passed only when the kind selects it, so kind=none disables reranking
            # regardless of a stray model id.
            reranking_model=(
                config.reranking_model
                if config.reranking_kind == "bedrock_rerank"
                else ""
            ),
            filter_builder=filter_builder,
            document_id_resolver=document_id_resolver,
        )
    elif config.kind is RetrievalKind.QDRANT:
        inner = _build_vector_engine(config, store_filter_keys)
    else:
        msg = f"unknown retrieval kind: {config.kind!r}"
        raise AppValueError(msg)
    return wrap_retrieval_engine(inner, config, policies=policies)


_SEAM_KINDS = {
    "filter_builder": RetrievalKind.BEDROCK,
    "document_id_resolver": RetrievalKind.BEDROCK,
    "store_filter_keys": RetrievalKind.QDRANT,
    "stub_passages": RetrievalKind.STUB,
}
"""The one kind each factory seam reaches."""


def _warn_about_seams(
    kind: RetrievalKind,
    policies: Sequence[PassagePolicy],
    *,
    filter_builder: FilterBuilder | None,
    document_id_resolver: DocumentIdResolver | None,
    store_filter_keys: Sequence[str],
    stub_passages: Sequence[RetrievalResult],
) -> None:
    """Log supplied seams the selected kind ignores (info), and warn about a scope not pushed down.

    Seams are per kind, so flipping ``kind`` (e.g. to qdrant for local development) silently drops the
    other kind's seams; and a ``MetadataEquals`` scope without a push-down lets the backend fill top_k
    from every scope before the policy drops the others, starving a scope of passages.
    """
    supplied = {
        "filter_builder": filter_builder is not None,
        "document_id_resolver": document_id_resolver is not None,
        "store_filter_keys": bool(store_filter_keys),
        "stub_passages": bool(stub_passages),
    }
    unused = [
        name
        for name, given in supplied.items()
        if given and _SEAM_KINDS[name] is not kind
    ]
    if unused:
        # Info, not warning: a composition root may pass every kind's seams so the kind can be flipped by
        # config alone. The failure that matters (a scope left without push-down) warns below.
        logger.info(
            "retrieval seams unused by the selected kind", kind=str(kind), seams=unused
        )
    scope_keys = [p.key for p in policies if isinstance(p, MetadataEquals)]
    if kind is RetrievalKind.QDRANT:
        # The vector engine pushes {k: request[k]} for each store filter key, so a scope is pushed down
        # only when its metadata key is one of them.
        scope_keys = [key for key in scope_keys if key not in store_filter_keys]
    elif kind is not RetrievalKind.BEDROCK or filter_builder is not None:
        # The stub has no backend top_k to starve; a Bedrock filter_builder is the consumer's push-down.
        scope_keys = []
    if scope_keys:
        logger.warning(
            "retrieval scope policies are not pushed down to the backend",
            kind=str(kind),
            scope_keys=scope_keys,
        )


def _build_vector_engine(
    config: RetrievalConfig, filter_keys: Sequence[str]
) -> RetrievalEngine:
    """Build the local vector-dev engine (Ollama embedder + Qdrant store) from ``config``.

    The embedding dimension is the single source of truth: both the embedder and the store are built
    with ``config.embedding_dimension``, and ``VectorRetrievalEngine`` asserts they agree. Heavy backends
    (httpx, qdrant-client) are imported lazily so the stub/bedrock paths never load them.
    """
    from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine  # noqa: PLC0415
    from techai_webutils.clients.vector.composition import build_embedder_and_store  # noqa: PLC0415

    embedder, store = build_embedder_and_store(
        embedding_host=config.embedding_host,
        embedding_model=config.embedding_model,
        embedding_dimension=config.embedding_dimension,
        vector_url=config.vector_url,
        collection=config.vector_collection,
    )
    return VectorRetrievalEngine(
        embedder, store, config.vector_collection, filter_keys=filter_keys
    )
