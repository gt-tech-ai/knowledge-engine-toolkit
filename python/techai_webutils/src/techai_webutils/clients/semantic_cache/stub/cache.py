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
    """Always misses, so the default graph behaves exactly as an uncached one.

    And needs no infra.
    """

    @override
    async def exact(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Miss.

        Args:
            query: The query, normalized before lookup.
            namespace: The tenant and model scope the answer must belong to.

        Returns:
            ``None``, always: this cache holds nothing.

        """
        return None

    @override
    async def nearest(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Miss.

        Args:
            query: The query.
            namespace: The tenant and model scope the answer must belong to.

        Returns:
            ``None``, always: this cache holds nothing.

        """
        return None

    @override
    async def store(
        self, query: str, payload: bytes, namespace: CacheNamespace, *, ttl_seconds: int
    ) -> None:
        """Discard the write.

        Args:
            query: The query the answer was generated for.
            payload: The answer, serialized.
            namespace: The tenant and model scope it belongs to.
            ttl_seconds: How long the answer lives.

        """

    @override
    async def invalidate_prefix(self, prefix: str) -> None:
        """Nothing is cached, so nothing to evict.

        Args:
            prefix: The key prefix whose entries are removed (for example one tenant's
                namespace).

        """

    @override
    async def invalidate_all(self) -> None:
        """Nothing is cached, so nothing to evict."""


_INTERFACE_CHECK: type[SemanticCache] = StubSemanticCache
"""Type-checker assertion that ``StubSemanticCache`` satisfies ``SemanticCache``."""
