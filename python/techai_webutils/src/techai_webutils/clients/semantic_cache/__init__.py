"""Semantic answer cache: the backend, its match tiers, the verifier and the admission rules.

A config-selected ``SemanticCache`` (``stub`` or ``redis``), the tiers that read it (``exact``,
``semantic``, ``verified``), the ``CacheVerifier`` and the ``CacheAdmission`` rules.
"""

from techai_webutils.clients.semantic_cache.admission import (
    CacheAdmissionConfig,
    CacheAdmissionKind,
    cache_admission_from_config,
)
from techai_webutils.clients.semantic_cache.builder import (
    SemanticCacheConfig,
    SemanticCacheKind,
    semantic_cache_from_config,
)
from techai_webutils.clients.semantic_cache.tiers import (
    CacheTierConfig,
    CacheTierKind,
    cache_lookup_from_config,
)
from techai_webutils.clients.semantic_cache.verifier import (
    CacheVerifierConfig,
    CacheVerifierKind,
    cache_verifier_from_config,
)

__all__ = [
    "CacheAdmissionConfig",
    "CacheAdmissionKind",
    "CacheTierConfig",
    "CacheTierKind",
    "CacheVerifierConfig",
    "CacheVerifierKind",
    "SemanticCacheConfig",
    "SemanticCacheKind",
    "cache_admission_from_config",
    "cache_lookup_from_config",
    "cache_verifier_from_config",
    "semantic_cache_from_config",
]
