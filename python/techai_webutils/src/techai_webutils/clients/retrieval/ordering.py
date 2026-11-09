"""Passage order: where the best passages sit in the prompt.

Models attend most to the start and the end of a long context. ``PassageOrder.edges`` puts
the best passage first, the second best last, the third second, and so on (ranks 1, 3, 5 …
4, 2), so the weakest sit in the middle. ``rank`` (the default) keeps retrieval order. The
reordering happens in the engine's output, so the prompt's ``[n]`` numbering and the
citations stay aligned.
"""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.retrieval import RetrievalResult


class PassageOrder(StrEnum):
    """How retrieved passages are ordered for the prompt."""

    RANK = "rank"
    """Best first (the default; today's behaviour)."""
    EDGES = "edges"
    """Best passages at both ends: ranks 1, 3, 5 … 4, 2."""


def order_passages[T](passages: Sequence[T], order: PassageOrder | str) -> list[T]:
    """Return ``passages`` (best first) in ``order``.

    Returns:
        ``passages`` (best first) in ``order``.

    Raises:
        AppValueError: An unknown order (``INVALID_INPUT``).

    """
    if order == PassageOrder.RANK:
        return list(passages)
    if order == PassageOrder.EDGES:
        return [*passages[0::2], *reversed(passages[1::2])]
    msg = f"unknown passage order: {order!r}"
    raise AppValueError(msg)


@final
class OrderingRetrievalEngine(DelegatingAsyncResource[RetrievalEngine], RetrievalEngine):
    """Reorders an engine's passages (after filtering) for the prompt."""

    def __init__(self, inner: RetrievalEngine, order: PassageOrder) -> None:
        """Wrap ``inner``; its results are returned in ``order``."""
        super().__init__(inner)
        self._order = order

    @override
    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine and reorder the passages.

        Returns:
            The retrieved passages.

        """
        results = await self._inner.retrieve(
            query, top_k=top_k, filters=filters, index_id=index_id
        )
        return order_passages(results, self._order)


def passage_header(number: int, passage: RetrievalResult) -> str:
    """Return a prompt header for passage ``number``.

    ``[n] <document> · <section> · p. <page>``.

    The section is the passage's ``section_path`` metadata; parts a passage lacks are left
    out.

    Returns:
        A prompt header for passage ``number``.

    """
    parts = [passage.document_name or passage.document_id]
    if section := passage.metadata.get("section_path"):
        parts.append(section)
    if passage.page_number is not None:
        parts.append(f"p. {passage.page_number}")
    return f"[{number}] " + " · ".join(parts)
