"""Index administration a reindex runs across a cell's domains: exact document counts and pruning.

A per-org reindex swaps the org's alias to its shadow index only once the shadow holds as many
documents as the source, and drops the previous index once its retention is over. Both act on
every domain of the cell at once, so they take the domain clients (``OpenSearchIndex.clients``
in a product's opener) rather than living on the stores, which already carry the read and write
surface.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.vector.opensearch.mapping import term_filters
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from opensearchpy import AsyncOpenSearch

COUNT_PAGE = 1000
"""Document ids per composite-aggregation page in ``count_org_documents``."""


async def count_org_documents(
    clients: Mapping[str, AsyncOpenSearch],
    index: str,
    org_id: str,
    *,
    org_field: str = "org_id",
) -> int:
    """Count ``org_id``'s distinct documents in ``index``, summed over every domain.

    Exact at any size: each domain's document ids are paged through a composite aggregation,
    so a shadow index that chunks documents differently from its source still compares by
    document. A document lives on one domain (its shard's), so the sum is exact; a domain
    without ``index`` counts 0.

    Args:
        clients: One client per domain of the cell.
        index: The index to count.
        org_id: The organization whose documents are counted.
        org_field: The field the org is stored in (``OpenSearchConfig.org_field``).

    Returns:
        The number of distinct documents.

    """
    counts = await asyncio.gather(
        *(_count(client, index, {org_field: org_id}) for client in clients.values())
    )
    return sum(counts)


async def _count(client: AsyncOpenSearch, index: str, fields: dict[str, str]) -> int:
    """Count the distinct ``document_id`` values matching ``fields`` in ``index`` on one domain."""
    query = {"bool": {"filter": term_filters(fields)}}
    after: object = None
    total = 0
    while True:
        composite: dict[str, object] = {
            "size": COUNT_PAGE,
            "sources": [{"doc": {"terms": {"field": "document_id"}}}],
        }
        if after is not None:
            composite["after"] = after
        response = await client.search(
            index=index,
            body={"size": 0, "query": query, "aggs": {"docs": {"composite": composite}}},
            params={"ignore_unavailable": "true"},
        )
        aggs = cast("dict[str, dict[str, object]]", response.get("aggregations", {}))
        docs = aggs.get("docs", {})
        total += len(cast("list[object]", docs.get("buckets", [])))
        after = docs.get("after_key")
        if after is None:
            return total


async def delete_unaliased_index(
    clients: Mapping[str, AsyncOpenSearch], index: str
) -> None:
    """Delete ``index`` on every domain, unless an alias on any domain still names it.

    Every domain is checked before any is touched, so a refusal deletes nothing. A domain
    without ``index`` is skipped.

    Args:
        clients: One client per domain of the cell.
        index: The index to delete.

    Raises:
        AppValueError: An alias still points at ``index`` (``INVALID_INPUT``).

    """
    named = await asyncio.gather(*(_aliases_of(c, index) for c in clients.values()))
    if aliases := sorted({alias for found in named for alias in found}):
        msg = f"index {index} is still aliased ({aliases}); not deleted"
        raise AppValueError(msg)
    await asyncio.gather(
        *(
            client.indices.delete(index=index, params={"ignore_unavailable": "true"})
            for client in clients.values()
        )
    )


async def _aliases_of(client: AsyncOpenSearch, index: str) -> list[str]:
    """Return the aliases naming ``index`` on one domain (none when it is absent)."""
    if not await client.indices.exists(index=index):
        return []
    found = await client.indices.get_alias(index=index)
    entry = cast("dict[str, dict[str, dict[str, object]]]", found).get(index, {})
    return sorted(entry.get("aliases", {}))
