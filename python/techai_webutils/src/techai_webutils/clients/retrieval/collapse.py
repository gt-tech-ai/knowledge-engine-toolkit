"""Collapsing duplicate passages, so copies of one text never crowd the top k.

Two passages are duplicates when their text is equal up to case and whitespace, or when
both carry the same value under ``cluster_key`` (a near-duplicate cluster id an indexer
stamps). Each group keeps its best-scored member, whose ``also_in`` metadata lists the
other members' document ids (comma-separated, in score order), so a citation can still
name every place the text lives. ``wrap_retrieval_engine`` applies it after the scope
policies, so a copy the caller may not see is dropped before it could be named.
"""

from __future__ import annotations

import hashlib
from dataclasses import replace
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.retrieval import RetrievalEngine
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.retrieval import RetrievalResult

ALSO_IN_KEY = "also_in"
"""The kept passage's metadata key listing the collapsed members' document ids."""


def collapse_duplicates(
    passages: Sequence[RetrievalResult], cluster_key: str = ""
) -> list[RetrievalResult]:
    """Return ``passages`` with each duplicate group reduced to its best-scored member.

    Best first.

    Args:
        passages: The passages, in any order.
        cluster_key: The metadata key of a near-duplicate cluster id; empty uses text
            only.

    Returns: One passage per group, best score first; a kept passage with collapsed
        members carries their document ids under ``also_in``.

    """
    kept: list[RetrievalResult] = []
    also: list[list[str]] = []
    group_of: dict[str, int] = {}
    for passage in sorted(passages, key=lambda p: -p.score):
        keys = _keys(passage, cluster_key)
        group = next((group_of[k] for k in keys if k in group_of), None)
        if group is None:
            group = len(kept)
            kept.append(passage)
            also.append([])
        elif passage.document_id not in {kept[group].document_id, *also[group]}:
            also[group].append(passage.document_id)
        for key in keys:
            group_of.setdefault(key, group)
    return [
        replace(p, metadata={**p.metadata, ALSO_IN_KEY: ",".join(others)})
        if others
        else p
        for p, others in zip(kept, also, strict=True)
    ]


def _keys(passage: RetrievalResult, cluster_key: str) -> list[str]:
    """Return the keys ``passage`` is grouped by.

    Its normalized text hash and its cluster id.
    """
    text = " ".join(passage.chunk_content.lower().split())
    keys = ["t:" + hashlib.sha256(text.encode()).hexdigest()]
    if cluster_key and (cluster := passage.metadata.get(cluster_key)):
        keys.append("c:" + cluster)
    return keys


class CollapsingRetrievalEngine(
    DelegatingAsyncResource[RetrievalEngine], RetrievalEngine
):
    """Collapses the inner engine's duplicate passages (``collapse_duplicates``)."""

    def __init__(self, inner: RetrievalEngine, *, cluster_key: str = "") -> None:
        """Wrap ``inner``; ``cluster_key`` names a near-duplicate cluster id.

        In the metadata.
        """
        self._inner = inner
        self._cluster_key = cluster_key

    async def retrieve(
        self,
        query: str,
        *,
        top_k: int = 10,
        filters: dict[str, str] | None = None,
        index_id: str | None = None,
    ) -> list[RetrievalResult]:
        """Retrieve from the inner engine and collapse its duplicates."""
        results = await self._inner.retrieve(
            query, top_k=top_k, filters=filters, index_id=index_id
        )
        return collapse_duplicates(results, self._cluster_key)
