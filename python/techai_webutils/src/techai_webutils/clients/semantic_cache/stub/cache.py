"""``StubSemanticCache`` — the default: every lookup misses, every write is a no-op."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.semantic_cache import SemanticCache

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.semantic_cache import (
        CachedAnswer,
        CacheNamespace,
    )


class StubSemanticCache(SemanticCache):
    """Always misses, so the default graph behaves exactly as an uncached one and needs no infra."""

    @override
    async def lookup(
        self, query: str, namespace: CacheNamespace, *, min_similarity: float
    ) -> CachedAnswer | None:
        """Miss."""
        return None

    @override
    async def store(
        self, query: str, payload: bytes, namespace: CacheNamespace, *, ttl_seconds: int
    ) -> None:
        """Discard the write."""

    @override
    async def invalidate_prefix(self, prefix: str) -> None:
        """Nothing is cached, so nothing to evict."""

    @override
    async def invalidate_all(self) -> None:
        """Nothing is cached, so nothing to evict."""


_INTERFACE_CHECK: type[SemanticCache] = StubSemanticCache
"""Type-checker assertion that ``StubSemanticCache`` satisfies ``SemanticCache``."""
