"""Citation extraction from retrieved passages.

Builds a structured ``Citation`` for **every** retrieved passage (not only those the answer's ``[n]``
markers reference), one-to-one and in retrieval order. The generator numbers passages 1-based in the
prompt, so the answer's inline ``[n]`` marker resolves to this list's n-th entry — which is why all
retrieved passages are surfaced as potential citations, and why entries are never collapsed.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.retrieval import Citation, CitationExtractor

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.retrieval import RetrievalResult


class PassageCitationExtractor(CitationExtractor):
    """Maps each retrieved passage to a Citation, one-to-one and in retrieval order."""

    def __init__(self, attribute_keys: Sequence[str] = ()) -> None:
        """Copy the passage metadata ``attribute_keys`` onto each citation's ``attributes``.

        The consumer names the keys its citations need (badges, links, icons); keys a passage lacks
        are omitted. No keys (the default) leaves ``attributes`` empty.
        """
        self._attribute_keys = tuple(attribute_keys)

    @override
    async def extract(
        self, answer: str, sources: list[RetrievalResult]
    ) -> list[Citation]:
        """Return one Citation per source passage, in retrieval order.

        The mapping is strictly positional: the generator numbers passages 1-based in the prompt, so
        the n-th entry here is the passage the answer's ``[n]`` marker cites. Collapsing entries (e.g.
        by document or page) would slide every later index onto the wrong passage and drop the
        citations the answer actually references, so distinct passages stay distinct citations.
        """
        return [
            Citation(
                document_id=source.document_id,
                document_name=source.document_name,
                chunk=source.chunk_content,
                page_number=source.page_number,
                confidence=source.score,
                attributes={
                    k: source.metadata[k]
                    for k in self._attribute_keys
                    if k in source.metadata
                },
            )
            for source in sources
        ]
