"""Config-selected retrieval engine factory (foundation/logger ``NewFromConfig`` pattern).

``RetrievalConfig.kind`` selects the engine (a stub, a local vector store, or a Bedrock
Knowledge Base). The selected engine is always wrapped in ``FilteringRetrievalEngine``
with the config's score floor plus the consumer's scope policies, so server-side
re-validation applies in every environment. Unknown kinds fail loudly. Heavy backends are
imported lazily, so the stub path never loads the AWS SDK or the vector clients.

Reranking is ``reranking_kind``: ``bedrock_rerank`` is Bedrock Retrieve's own reranker
(the bedrock kind only — any other kind refuses it); ``cross_encoder`` wraps any kind's
engine in ``RerankingRetrievalEngine`` over the caller's ``Reranker``
(``clients.reranking``), before the abstainer, so the threshold reads the reranker's
scores.

Push-down is explicit: a ``MetadataEquals`` scope reaches the backend only through that
kind's seam (``filter_builder`` for Bedrock, ``store_filter_keys`` for the vector store);
otherwise the backend returns the index-wide top_k and the policy post-filters it. The
factory warns when a scope is not pushed down, and logs (at info) a supplied seam the
selected kind does not use.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.retrieval.abstain import (
    AbstainerKind,
    TopScoreAbstainer,
    abstention_policies,
)
from techai_webutils.clients.retrieval.collapse import CollapsingRetrievalEngine
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
)
from techai_webutils.clients.retrieval.fusion import FusionConfig
from techai_webutils.clients.retrieval.ordering import (
    OrderingRetrievalEngine,
    PassageOrder,
)
from techai_webutils.clients.retrieval.reranking import (
    RerankingKind,
    RerankingRetrievalEngine,
)
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine
from techai_webutils.clients.retrieval.vector import VectorRetrievalEngine
from techai_webutils.core.errors import AppError, AppTypeError, AppValueError, ErrorCode
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Callable, Sequence

    from techai_webutils.clients.retrieval.bedrock.engine import (
        BedrockRetrievalEngine,
        BedrockRetrievalOptions,
        DocumentIdResolver,
    )
    from techai_webutils.clients.retrieval.bedrock.filters import FilterBuilder
    from techai_webutils.clients.retrieval.filtering import PassagePolicy
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.reranker import Reranker
    from techai_webutils.core.interfaces.retrieval import RetrievalEngine, RetrievalResult
    from techai_webutils.core.interfaces.vector_store import VectorStore

logger = get_logger(__name__)


class RetrievalKind(StrEnum):
    """Which retrieval engine implementation to build."""

    STUB = "stub"
    """The in-memory engine over a consumer-supplied corpus (local development and
    tests)."""
    BEDROCK = "bedrock"
    """The AWS Bedrock Knowledge Base retrieval engine."""
    QDRANT = "qdrant"
    """A Qdrant vector store fed by an Ollama embedder (local vector development)."""
    OPENSEARCH = "opensearch"
    """The injected OpenSearch vector store: k-NN and BM25 fused (``fusion``)."""


@dataclass(frozen=True, slots=True)
class RetrievalConfig:
    """Retrieval engine configuration.

    The consumer maps it from its own config section.
    """

    kind: RetrievalKind = RetrievalKind.STUB
    """Selects the engine implementation."""
    region: str = "us-east-1"
    """AWS region for the Bedrock KB client (unused by the stub)."""
    endpoint: str | None = None
    """Optional Bedrock endpoint override (e.g. a VPC endpoint); ``None`` uses AWS default
    resolution."""
    knowledge_base_id: str = ""
    """The Bedrock Knowledge Base id to query (empty for the stub)."""
    search_type: str = "semantic"
    """KB search strategy: "semantic" (vector only) | "hybrid" (vector + keyword; the KB's
    OpenSearch storage supports it). "hybrid" sets Retrieve's overrideSearchType."""
    reranking_kind: str = RerankingKind.NONE
    """Reranker selection (``RerankingKind``): "none" | "bedrock_rerank" (a Cohere Rerank
    model reorders the candidate pool via Retrieve's rerankingConfiguration; the bedrock
    kind only) | "cross_encoder" (the caller's ``Reranker`` reorders any kind's passages).
    Another value is refused."""
    reranking_model: str = ""
    """Reranker model id for the bedrock_rerank kind, e.g. "cohere.rerank-v3-5:0" (empty →
    no reranking)."""
    min_score: float = 0.5
    """Relevance floor for the filtering decorator; passages below it are dropped
    (including a stub's hand-written passages). Backends score on different scales —
    Bedrock Knowledge Base scores run lower than a local vector store's, so the 0.5
    default can drop strong Bedrock matches — so set it per engine kind. ``None`` is
    rejected (there is no per-kind default)."""
    # Local vector stack (``kind == QDRANT``): the caller injects the embedder and the
    # store; these name what they read. Ignored by the stub/bedrock kinds.
    vector_collection: str = "documents"
    """Qdrant collection name holding the document embeddings."""
    embedding_model: str = "nomic-embed-text"
    """Embedding model name requested from Ollama."""
    fusion: FusionConfig = field(default_factory=FusionConfig)
    """How a store with keyword search fuses its k-NN and BM25 rankings
    (``opensearch``)."""
    abstainer: AbstainerKind = AbstainerKind.SCORE_THRESHOLD
    """``score_threshold`` drops passages below ``min_score`` (the default); ``top_score``
    drops the whole pool when its best passage is below ``abstain_threshold``; ``none``
    keeps all, e.g. for fused RRF scores, which are not on ``min_score``'s scale."""
    abstain_threshold: float = 0.0
    """The ``top_score`` bar, fitted for a target precision on the score the engine
    returns (the reranker's when reranking is on); required above 0 by ``top_score``,
    unused otherwise."""
    passage_order: PassageOrder = PassageOrder.RANK
    """The order passages reach the prompt: best first (default) or best at both ends."""
    collapse_duplicates: bool = False
    """Collapse passages with the same text (or cluster id) into the best-scored one,
    which lists the others' document ids under ``also_in``
    (``clients.retrieval.collapse``); off keeps all."""
    collapse_cluster_key: str = ""
    """The metadata key of a near-duplicate cluster id the collapse also groups by; empty:
    text."""

    def __post_init__(self) -> None:
        """Reject a non-numeric ``min_score`` or an unknown ``reranking_kind``.

        ``None`` is non-numeric.

        Raises:
            AppTypeError: ``min_score`` is not a number.
            AppValueError: ``reranking_kind`` is not a ``RerankingKind`` value, or
                ``top_score`` has no positive ``abstain_threshold`` (``INVALID_INPUT``).

        """
        # Read as ``object``: the annotation says float, but a caller can still pass None.
        min_score = cast("object", self.min_score)
        if isinstance(min_score, bool) or not isinstance(min_score, int | float):
            msg = f"RetrievalConfig.min_score must be a number, got {min_score!r}"
            raise AppTypeError(msg)
        if self.reranking_kind not in set(RerankingKind):
            msg = f"unknown reranking kind: {self.reranking_kind!r}"
            raise AppValueError(msg)
        if self.abstainer == AbstainerKind.TOP_SCORE and self.abstain_threshold <= 0:
            msg = "the top_score abstainer needs a positive abstain_threshold"
            raise AppValueError(msg)


def wrap_retrieval_engine(
    inner: RetrievalEngine,
    config: RetrievalConfig,
    *,
    policies: Sequence[PassagePolicy],
    reranker: Reranker | None = None,
) -> RetrievalEngine:
    """Wrap ``inner`` in the factory's reranking, filtering and ordering.

    With ``reranking_kind`` ``cross_encoder``, ``reranker`` first reorders the passages
    and sets their scores. The filter then applies the abstainer
    (``MinScore(config.min_score)`` by default), then ``policies``; with
    ``collapse_duplicates`` the in-scope duplicates then collapse (so an out-of-scope copy
    is never named); ``top_score`` then drops a pool whose best passage is weak; the
    passages are then put in ``config.passage_order``.
    ``new_retrieval_engine_from_config`` wraps every kind it builds with this; a consumer
    with its own engine (a custom corpus, a test double of the backend) calls it directly
    so that engine is scoped exactly like the factory's.

    Args:
        inner: The bare engine.
        config: The retrieval settings.
        policies: The passage policies, applied in order.
        reranker: The reranker; ``None`` skips reranking.

    Returns:
        The wrapped engine.

    Raises:
        AppValueError: ``cross_encoder`` without a ``reranker`` (``INVALID_INPUT``).

    """
    if config.reranking_kind == RerankingKind.CROSS_ENCODER:
        if reranker is None:
            msg = "the cross_encoder reranking kind needs a reranker built by the caller"
            raise AppValueError(msg)
        inner = RerankingRetrievalEngine(inner, reranker)
    scoped: RetrievalEngine = FilteringRetrievalEngine(
        inner, [*abstention_policies(config.abstainer, config.min_score), *policies]
    )
    if config.collapse_duplicates:
        scoped = CollapsingRetrievalEngine(
            scoped, cluster_key=config.collapse_cluster_key
        )
    if config.abstainer == AbstainerKind.TOP_SCORE:
        scoped = TopScoreAbstainer(scoped, config.abstain_threshold)
    if config.passage_order == PassageOrder.RANK:
        return scoped
    return OrderingRetrievalEngine(scoped, config.passage_order)


@dataclass(frozen=True, kw_only=True)
class _InnerEngineSeams:
    """What ``_inner_engine`` takes, grouped into one value."""

    filter_builder: FilterBuilder | None
    """The ``filter_builder`` value of ``_inner_engine``."""

    document_id_resolver: DocumentIdResolver | None
    """The ``document_id_resolver`` value of ``_inner_engine``."""

    store_filter_keys: Sequence[str]
    """The ``store_filter_keys`` value of ``_inner_engine``."""

    stub_passages: Sequence[RetrievalResult]
    """The ``stub_passages`` value of ``_inner_engine``."""

    embedder: EmbeddingProvider | None
    """The ``embedder`` value of ``_inner_engine``."""

    store: VectorStore | None
    """The ``store`` value of ``_inner_engine``."""


def _inner_engine(config: RetrievalConfig, options: _InnerEngineSeams) -> RetrievalEngine:
    """Build the unwrapped engine ``config.kind`` selects.

    Args:
        config: Selects the backend and carries its settings.
        options: The seams the backends need.

    Returns:
        The engine for the selected kind.

    Raises:
        AppValueError: The kind is unknown.

    """
    if config.kind is RetrievalKind.STUB:
        return StubRetrievalEngine(options.stub_passages)
    if config.kind is RetrievalKind.BEDROCK:
        return _build_bedrock_engine(
            config, options.filter_builder, options.document_id_resolver
        )
    if config.kind in _VECTOR_KINDS:
        return _build_vector_engine(
            config, options.store_filter_keys, options.embedder, options.store
        )
    msg = f"unknown retrieval kind: {config.kind!r}"
    raise AppValueError(msg)


@dataclass(frozen=True, kw_only=True)
class RetrievalEngineDependencies:
    """What ``new_retrieval_engine_from_config`` takes, grouped into one value."""

    policies: Sequence[PassagePolicy]
    """The ``policies`` value of ``new_retrieval_engine_from_config``."""

    filter_builder: FilterBuilder | None = None
    """The ``filter_builder`` value of ``new_retrieval_engine_from_config``."""

    document_id_resolver: DocumentIdResolver | None = None
    """The ``document_id_resolver`` value of ``new_retrieval_engine_from_config``."""

    store_filter_keys: Sequence[str] = ()
    """The ``store_filter_keys`` value of ``new_retrieval_engine_from_config``."""

    stub_passages: Sequence[RetrievalResult] = ()
    """The ``stub_passages`` value of ``new_retrieval_engine_from_config``."""

    embedder: EmbeddingProvider | None = None
    """The ``embedder`` value of ``new_retrieval_engine_from_config``."""

    store: VectorStore | None = None
    """The ``store`` value of ``new_retrieval_engine_from_config``."""

    reranker: Reranker | None = None
    """The ``reranker`` value of ``new_retrieval_engine_from_config``."""


def new_retrieval_engine_from_config(
    config: RetrievalConfig, options: RetrievalEngineDependencies
) -> RetrievalEngine:
    """Build the retrieval engine ``config.kind`` selects, wrapped for policy and order.

    The wrapping applies ``MinScore(config.min_score)`` plus ``policies`` — the consumer's
    scope rules (e.g. ``MetadataEquals`` on its tenant key); pass an empty list only when
    there is no scope to enforce. The other seams reach one kind each: ``filter_builder``
    and ``document_id_resolver`` the Bedrock engine, ``store_filter_keys`` the vector
    engine's push-down, ``stub_passages`` the stub's corpus, and ``embedder`` and
    ``store`` the vector engine (the caller builds them with ``clients.embedding`` and
    ``clients.vector``; a client family never imports a sibling). ``reranker``
    (``clients.reranking``) reorders any kind's passages when ``reranking_kind`` is
    ``cross_encoder``. A ``MetadataEquals`` scope with no push-down seam for the selected
    kind is logged as a warning; a seam the selected kind ignores is logged at info. A
    callee raises ``INVALID_INPUT`` on a bad kind or reranker pairing.

    Args:
        config: Selects the backend and carries its settings.
        options: The seams, policies and collaborators.

    Returns:
        The retrieval engine ``config.kind`` selects, wrapped for policy and order.

    """
    _refuse_bedrock_rerank_off_bedrock(config)
    _warn_about_seams(
        config.kind,
        options.policies,
        options=_SeamWarnings(
            filter_builder=options.filter_builder,
            document_id_resolver=options.document_id_resolver,
            store_filter_keys=options.store_filter_keys,
            stub_passages=options.stub_passages,
        ),
    )
    inner = _inner_engine(
        config,
        options=_InnerEngineSeams(
            filter_builder=options.filter_builder,
            document_id_resolver=options.document_id_resolver,
            store_filter_keys=options.store_filter_keys,
            stub_passages=options.stub_passages,
            embedder=options.embedder,
            store=options.store,
        ),
    )
    return wrap_retrieval_engine(
        inner, config, policies=options.policies, reranker=options.reranker
    )


def _refuse_bedrock_rerank_off_bedrock(config: RetrievalConfig) -> None:
    """Refuse ``bedrock_rerank`` on a kind other than ``bedrock``.

    It reranks inside Bedrock Retrieve.

    Args:
        config: The retrieval settings.

    Raises:
        AppValueError: The misplaced reranker (``INVALID_INPUT``).

    """
    if (
        config.reranking_kind == RerankingKind.BEDROCK_RERANK
        and config.kind is not RetrievalKind.BEDROCK
    ):
        msg = (
            f"bedrock_rerank reranks inside Bedrock Retrieve; the {config.kind} kind "
            f"needs cross_encoder"
        )
        raise AppValueError(msg)


def _build_bedrock_engine(
    config: RetrievalConfig,
    filter_builder: FilterBuilder | None,
    document_id_resolver: DocumentIdResolver | None,
) -> RetrievalEngine:
    """Build the Bedrock Knowledge Base engine; aiobotocore is loaded only on this path.

    Args:
        config: The retrieval settings.
        filter_builder: Builds the request's filter; ``None`` sends none.
        document_id_resolver: Names a passage's document; ``None`` uses the default.

    Returns:
        The Bedrock Knowledge Base engine.

    """
    bedrock = importlib.import_module("techai_webutils.clients.retrieval.bedrock.engine")
    engine = cast("type[BedrockRetrievalEngine]", bedrock.BedrockRetrievalEngine)
    return engine(
        options=cast(
            "Callable[..., BedrockRetrievalOptions]", bedrock.BedrockRetrievalOptions
        )(
            region=config.region,
            knowledge_base_id=config.knowledge_base_id,
            endpoint=config.endpoint,
            search_type=config.search_type,
            # The reranker model is passed only when the kind selects it, so kind=none
            # disables reranking regardless of a stray model id.
            reranking_model=(
                config.reranking_model
                if config.reranking_kind == "bedrock_rerank"
                else ""
            ),
            filter_builder=filter_builder,
            document_id_resolver=document_id_resolver,
        )
    )


_VECTOR_KINDS = frozenset({RetrievalKind.QDRANT, RetrievalKind.OPENSEARCH})
"""The kinds built as a vector engine over the injected embedder and store."""

_SEAM_KINDS = {
    "filter_builder": frozenset({RetrievalKind.BEDROCK}),
    "document_id_resolver": frozenset({RetrievalKind.BEDROCK}),
    "store_filter_keys": _VECTOR_KINDS,
    "stub_passages": frozenset({RetrievalKind.STUB}),
}
"""The kinds each factory seam reaches."""


@dataclass(frozen=True, kw_only=True)
class _SeamWarnings:
    """What ``_warn_about_seams`` takes, grouped into one value."""

    filter_builder: FilterBuilder | None
    """The ``filter_builder`` value of ``_warn_about_seams``."""

    document_id_resolver: DocumentIdResolver | None
    """The ``document_id_resolver`` value of ``_warn_about_seams``."""

    store_filter_keys: Sequence[str]
    """The ``store_filter_keys`` value of ``_warn_about_seams``."""

    stub_passages: Sequence[RetrievalResult]
    """The ``stub_passages`` value of ``_warn_about_seams``."""


def _warn_about_seams(
    kind: RetrievalKind, policies: Sequence[PassagePolicy], options: _SeamWarnings
) -> None:
    """Log seams the selected kind ignores and warn about a scope not pushed down.

    Seams are per kind, so flipping ``kind`` (e.g. to qdrant for local development)
    silently drops the other kind's seams; and a ``MetadataEquals`` scope without a
    push-down lets the backend fill top_k from every scope before the policy drops the
    others, starving a scope of passages.

    Args:
        kind: The selected kind.
        policies: The passage policies.
        options: The seams the caller passed.

    """
    supplied = {
        "filter_builder": options.filter_builder is not None,
        "document_id_resolver": options.document_id_resolver is not None,
        "store_filter_keys": bool(options.store_filter_keys),
        "stub_passages": bool(options.stub_passages),
    }
    unused = [
        name
        for name, given in supplied.items()
        if given and kind not in _SEAM_KINDS[name]
    ]
    if unused:
        # Info, not warning: a composition root may pass every kind's seams so the kind
        # can be flipped by config alone. The failure that matters (a scope left without
        # push-down) warns below.
        logger.info(
            "retrieval seams unused by the selected kind", kind=str(kind), seams=unused
        )
    scope_keys = [p.key for p in policies if isinstance(p, MetadataEquals)]
    if kind in _VECTOR_KINDS:
        # The vector engine pushes {k: request[k]} for each store filter key, so a scope
        # is pushed down only when its metadata key is one of them.
        scope_keys = [key for key in scope_keys if key not in options.store_filter_keys]
    elif kind is not RetrievalKind.BEDROCK or options.filter_builder is not None:
        # The stub has no backend top_k to starve; a Bedrock filter_builder is the
        # consumer's push-down.
        scope_keys = []
    if scope_keys:
        logger.warning(
            "retrieval scope policies are not pushed down to the backend",
            kind=str(kind),
            scope_keys=scope_keys,
        )


def _build_vector_engine(
    config: RetrievalConfig,
    filter_keys: Sequence[str],
    embedder: EmbeddingProvider | None,
    store: VectorStore | None,
) -> RetrievalEngine:
    """Build the vector engine over the caller's embedder and store.

    It checks their dimensions agree.

    Args:
        config: The retrieval settings.
        filter_keys: The request fields pushed down as filters.
        embedder: Embeds the query; required.
        store: Holds the vectors; required.

    Returns:
        The vector engine over the caller's embedder and store.

    Raises:
        AppError: ``INVALID_INPUT`` when the embedder or the store is missing.

    """
    if embedder is None or store is None:
        raise AppError(
            ErrorCode.INVALID_INPUT,
            f"the {config.kind} retrieval kind needs an embedder and a store built "
            f"by the caller",
        )
    return VectorRetrievalEngine(
        embedder,
        store,
        config.vector_collection,
        filter_keys=filter_keys,
        fusion=config.fusion,
    )
