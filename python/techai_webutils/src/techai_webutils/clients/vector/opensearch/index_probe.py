"""``probe_index``: check that an org's new index serves what its previous index served.

A reindex or a sink flip swaps an org's reads to a new index. Before that swap is trusted,
a probe samples the org's chunks and asks three questions, each a share in [0, 1]:

- **known-answer recall:** a sampled chunk's own text, as a lexical query on the new
  index, finds the chunk's document in the top ``k``. The samples come from the previous
  index when there is one (the documents users could find before), else from the new
  index.
- **overlap:** for the same queries, the share of the previous index's top-``k`` documents
  the new index also returns (1.0 with no previous index).
- **self-recall:** a chunk sampled from the new index, searched by its own stored vector,
  finds its document in the top ``k``. The vector is the new index's own, so a model
  change is judged fairly.

Samples are random but seeded, so a re-run probes the same chunks. A query's text is cut
to its first ``query_words`` words. Every domain of the cell is searched and the hits
merged by score, as retrieval does. An org with no chunks to sample scores 1.0 with zero
samples: there is nothing to lose.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.vector.opensearch.mapping import (
    EMBEDDING_FIELD,
    TEXT_FIELD,
    knn_query,
    term_filters,
)

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from opensearchpy import AsyncOpenSearch

_DOCUMENT_FIELD = "document_id"
"""The chunk field naming its document."""


@dataclass(frozen=True, slots=True)
class ProbeSettings:
    """How much a probe samples and how deep it looks."""

    sample_size: int = 20
    """Chunks sampled per question."""
    k: int = 10
    """How deep in the results a document counts as found."""
    query_words: int = 64
    """A lexical query's text is cut to this many words."""
    seed: int = 0
    """The sampling seed; the same seed samples the same chunks."""


@dataclass(frozen=True, slots=True)
class IndexProbe:
    """What a probe found."""

    samples: int
    """The lexical queries run (0 when the org had no chunks to sample)."""
    known_answer_recall: float
    """Share of sampled chunks whose document the new index returns in the top ``k``."""
    overlap: float
    """Mean share of the previous index's top-``k`` documents the new index also
    returns."""
    self_recall: float
    """Share of the new index's sampled chunks found by their own vector in the top
    ``k``."""


@dataclass(frozen=True, slots=True)
class _Chunk:
    """One sampled chunk."""

    document_id: str
    """The chunk's document."""
    text: str
    """The chunk's text."""
    vector: list[float]
    """The chunk's stored vector."""


@dataclass(frozen=True, kw_only=True)
class ProbeIndexOptions:
    """What ``probe_index`` takes, grouped into one value."""

    org_field: str = "org_id"
    """The field the org is stored in (``OpenSearchConfig.org_field``)."""

    settings: ProbeSettings | None = None
    """The sample size, depth, query length and seed (the defaults when None)."""


async def probe_index(
    clients: Mapping[str, AsyncOpenSearch],
    org_id: str,
    current: str,
    previous: str = "",
    *,
    options: ProbeIndexOptions,
) -> IndexProbe:
    """Probe ``current`` for ``org_id``, against ``previous`` when one is given.

    Args:
        clients: One client per domain of the cell.
        org_id: The organization.
        current: The index the org now reads.
        previous: The index it read before; empty when there is none (a first index or a
            sink flip into a shared index).
        options: The settings and collaborators, grouped.

    Returns:
        The probe's three shares and its sample count.

    """
    s = options.settings or ProbeSettings()
    fields = {options.org_field: org_id}
    source = previous or current
    queries = await _sample(clients, source, fields, s, with_vector=False)
    current_chunks = await _sample(clients, current, fields, s, with_vector=True)
    found = 0
    overlaps: list[float] = []
    for chunk in queries:
        text = " ".join(chunk.text.split()[: s.query_words])
        now = await _lexical(clients, current, fields, text, s.k)
        found += chunk.document_id in now
        if previous:
            before = await _lexical(clients, previous, fields, text, s.k)
            overlaps.append(
                len(set(before) & set(now)) / len(set(before)) if before else 1.0
            )
    self_found = 0
    for chunk in current_chunks:
        body = knn_query(chunk.vector, s.k, fields)
        self_found += chunk.document_id in await _documents(clients, current, body, s.k)
    return IndexProbe(
        samples=len(queries),
        known_answer_recall=found / len(queries) if queries else 1.0,
        overlap=sum(overlaps) / len(overlaps) if overlaps else 1.0,
        self_recall=self_found / len(current_chunks) if current_chunks else 1.0,
    )


async def _sample(
    clients: Mapping[str, AsyncOpenSearch],
    index: str,
    fields: Mapping[str, str],
    settings: ProbeSettings,
    *,
    with_vector: bool,
) -> list[_Chunk]:
    """Return up to ``sample_size`` seeded-random chunks of the org in ``index``.

    Args:
        clients: One client per domain.
        index: The index to sample.
        fields: The org filter.
        settings: The sample size and seed.
        with_vector: Whether to read each chunk's stored vector.

    Returns:
        The sampled chunks with a document id and text (and a vector when asked).

    """
    wanted = [_DOCUMENT_FIELD, TEXT_FIELD, *([EMBEDDING_FIELD] if with_vector else [])]
    body = {
        "size": settings.sample_size,
        "_source": wanted,
        "query": {
            "function_score": {
                "query": {"bool": {"filter": term_filters(fields)}},
                "random_score": {"seed": settings.seed, "field": "_seq_no"},
                "boost_mode": "replace",
            }
        },
    }
    hits = await _hits(clients, index, body)
    chunks = [_chunk(hit) for hit in hits[: settings.sample_size]]
    return [
        c for c in chunks if c.document_id and c.text and (c.vector or not with_vector)
    ]


async def _lexical(
    clients: Mapping[str, AsyncOpenSearch],
    index: str,
    fields: Mapping[str, str],
    text: str,
    k: int,
) -> list[str]:
    """Return the top-``k`` document ids of a lexical query for ``text`` in ``index``.

    Args:
        clients: One client per domain.
        index: The index to search.
        fields: The org filter.
        text: The query text.
        k: How many hits to keep.

    Returns:
        The documents of the best ``k`` hits, best first.

    """
    body = {
        "size": k,
        "_source": [_DOCUMENT_FIELD],
        "query": {
            "bool": {
                "must": [{"match": {TEXT_FIELD: text}}],
                "filter": term_filters(fields),
            }
        },
    }
    return await _documents(clients, index, body, k)


async def _documents(
    clients: Mapping[str, AsyncOpenSearch], index: str, body: Mapping[str, object], k: int
) -> list[str]:
    """Run ``body`` on every domain and return the documents of the best ``k`` hits.

    Args:
        clients: One client per domain.
        index: The index to search.
        body: The search body.
        k: How many hits to keep.

    Returns:
        The hits' document ids, best first.

    """
    hits = await _hits(clients, index, body)
    return [_chunk(hit).document_id for hit in hits[:k]]


async def _hits(
    clients: Mapping[str, AsyncOpenSearch], index: str, body: Mapping[str, object]
) -> list[dict[str, object]]:
    """Run ``body`` on every domain and return all hits, best score first.

    Args:
        clients: One client per domain.
        index: The index to search; a domain without it contributes nothing.
        body: The search body.

    Returns:
        The merged hits.

    """
    responses = await asyncio.gather(
        *(
            client.search(
                index=index, body=dict(body), params={"ignore_unavailable": "true"}
            )
            for client in clients.values()
        )
    )
    hits: list[dict[str, object]] = []
    for response in cast("list[dict[str, object]]", responses):
        outer = cast("dict[str, object]", response.get("hits", {}))
        hits.extend(cast("Sequence[dict[str, object]]", outer.get("hits", [])))
    return sorted(hits, key=lambda hit: -_score(hit))


def _score(hit: Mapping[str, object]) -> float:
    """Return a hit's score (0 when absent).

    Args:
        hit: A search hit.

    Returns:
        A hit's score (0 when absent).

    """
    score = hit.get("_score")
    return float(score) if isinstance(score, int | float) else 0.0


def _chunk(hit: Mapping[str, object]) -> _Chunk:
    """Read a hit's document id, text and vector from its ``_source``.

    Args:
        hit: One search hit.

    Returns:
        The chunk; a missing field reads empty.

    """
    source = cast("dict[str, object]", hit.get("_source", {}))
    document = source.get(_DOCUMENT_FIELD)
    text = source.get(TEXT_FIELD)
    vector = source.get(EMBEDDING_FIELD)
    return _Chunk(
        document_id=document if isinstance(document, str) else "",
        text=text if isinstance(text, str) else "",
        vector=[float(v) for v in cast("list[float]", vector)]
        if isinstance(vector, list)
        else [],
    )
