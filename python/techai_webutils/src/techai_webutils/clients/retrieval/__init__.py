"""Retrieval-engine clients.

(adapters behind ``core.interfaces.retrieval.RetrievalEngine``).

The raw engines — Bedrock Knowledge Base, a local vector store and a stub — the
policy-based ``FilteringRetrievalEngine`` decorator with its built-in ``PassagePolicy``
rules, the config-selected ``new_retrieval_engine_from_config`` factory, and
``wrap_retrieval_engine``, which gives a consumer's own engine the factory's filtering and
passage order. Hybrid engines fuse rankings with ``FusionConfig``
(``reciprocal_rank_fusion``, ``merge_by_score`` for shards); ``AbstainerKind`` and
``PassageOrder`` select abstention and prompt order (``passage_header`` names a passage in
the prompt); ``collapse_duplicates`` keeps one passage per duplicate group, naming the
rest under ``ALSO_IN_KEY``. Apps select an engine via config and depend only on the
interface. The Bedrock seam types (``FilterBuilder``, ``DocumentIdResolver``,
``metadata_document_id``) are exported by ``clients.retrieval.bedrock``, which loads the
AWS SDK.
"""

from techai_webutils.clients.retrieval.abstain import AbstainerKind
from techai_webutils.clients.retrieval.builder import (
    RetrievalConfig,
    RetrievalKind,
    new_retrieval_engine_from_config,
    wrap_retrieval_engine,
)
from techai_webutils.clients.retrieval.collapse import ALSO_IN_KEY, collapse_duplicates
from techai_webutils.clients.retrieval.filtering import (
    FilteringRetrievalEngine,
    MetadataEquals,
    MinScore,
    OrdinalCeiling,
    PassagePolicy,
)
from techai_webutils.clients.retrieval.fusion import (
    FusionConfig,
    FusionKind,
    Normalization,
    fuse,
    merge_by_score,
    reciprocal_rank_fusion,
)
from techai_webutils.clients.retrieval.ordering import (
    PassageOrder,
    order_passages,
    passage_header,
)
from techai_webutils.clients.retrieval.stub import StubRetrievalEngine

__all__ = [
    "ALSO_IN_KEY",
    "AbstainerKind",
    "FilteringRetrievalEngine",
    "FusionConfig",
    "FusionKind",
    "MetadataEquals",
    "MinScore",
    "Normalization",
    "OrdinalCeiling",
    "PassageOrder",
    "PassagePolicy",
    "RetrievalConfig",
    "RetrievalKind",
    "StubRetrievalEngine",
    "collapse_duplicates",
    "fuse",
    "merge_by_score",
    "new_retrieval_engine_from_config",
    "order_passages",
    "passage_header",
    "reciprocal_rank_fusion",
    "wrap_retrieval_engine",
]
