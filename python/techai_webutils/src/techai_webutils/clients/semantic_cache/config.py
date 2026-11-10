"""The semantic cache's kinds and configuration, shared by the factory and its backends.

A leaf module: ``builder`` selects a backend from ``SemanticCacheConfig`` and each backend
reads it, so the config lives where neither imports the other.
"""

from dataclasses import dataclass
from enum import StrEnum


class SemanticCacheKind(StrEnum):
    """The semantic cache backends."""

    STUB = "stub"
    """Always miss (the default; zero infrastructure)."""
    REDIS = "redis"
    """KV payloads + a cosine KNN index on the same server (RediSearch or Valkey
    Search)."""
    # QDRANT reserved: a vector-store-backed index, when a deployment has no search
    # module.


@dataclass(frozen=True, slots=True)
class SemanticCacheConfig:
    """Semantic cache configuration."""

    kind: str = SemanticCacheKind.STUB
    """A ``SemanticCacheKind`` value."""
    min_similarity: float = 0.95
    """The cosine similarity a cached query must reach to count as a hit."""
    ttl_seconds: int = 3600
    """How long a cached answer lives."""
    index_prefix: str = "sc"
    """Key prefix for the payload keys, index documents and the index name."""
    normalize: bool = True
    """Lower-case and whitespace-collapse queries before keying them (store and ``exact``
    agree)."""
    strip_trailing_punctuation: bool = False
    """Also drop trailing punctuation (``?``, ``.``, ``!``) when normalizing, so "What is
    X?" and "what is x" are one exact repeat."""
