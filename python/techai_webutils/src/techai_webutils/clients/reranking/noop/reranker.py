"""``NoopReranker``: keeps the retriever's order (the factory's default)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.reranker import Reranker
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence


class NoopReranker(NoOpAsyncResource, Reranker):
    """Scores passages by their position, so a sort by score keeps the input order."""

    @override
    async def rerank(self, query: str, passages: Sequence[str]) -> list[float]:
        """Return ``1.0`` for the first passage.

        Falling evenly towards ``0`` for the last.

        Args:
            query: The search query.
            passages: The candidate passages.

        Returns:
            ``1.0`` for the first passage.

        """
        count = len(passages)
        return [(count - i) / count for i in range(count)]
