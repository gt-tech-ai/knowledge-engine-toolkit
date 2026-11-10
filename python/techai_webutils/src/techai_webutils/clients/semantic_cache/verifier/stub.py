"""``StubCacheVerifier``: the default, which confirms nothing.

The verified band therefore serves nothing.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.semantic_cache import CacheVerifier


class StubCacheVerifier(CacheVerifier):
    """Never confirms.

    A listed ``verified`` tier is inert until a model verifier is chosen.
    """

    @override
    async def same_question(self, cached_query: str, query: str) -> bool:
        """Return ``False``.

        Args:
            cached_query: The query the cached answer was stored for.
            query: The new query.

        Returns:
            ``False``.

        """
        return False


_INTERFACE_CHECK: type[CacheVerifier] = StubCacheVerifier
"""Type-checker assertion that ``StubCacheVerifier`` satisfies ``CacheVerifier``."""
