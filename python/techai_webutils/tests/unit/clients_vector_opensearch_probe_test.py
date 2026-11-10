"""Unit tests for ``probe_index``.

(the OpenSearch client mocked at its ``search`` call).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.vector.opensearch.index_probe import (
    IndexProbe,
    ProbeIndexOptions,
    ProbeSettings,
    probe_index,
)
from tests.json_path import dig

if TYPE_CHECKING:
    from opensearchpy import AsyncOpenSearch

_EXPECTED_SAMPLE = 7

_ORG = "org-a"
_OLD = "chunks-old"
_NEW = "chunks-new"


def _hit(document: str, score: float, *, vector: bool = False) -> dict[str, object]:
    """Return a search hit for ``document`` with a text and, when asked, a vector.

    Args:
        document: The document.
        score: Its score.
        vector: Whether the hit carries its vector.

    Returns:
        A search hit for ``document`` with a text and, when asked, a vector.

    """
    source: dict[str, object] = {
        "document_id": document,
        "text": f"text of {document} " * 3,
    }
    if vector:
        source["embedding"] = [0.1, 0.2]
    return {"_score": score, "_source": source}


def _reply(hits: list[dict[str, object]]) -> dict[str, object]:
    """Return a search response holding ``hits``.

    Args:
        hits: The hits.

    Returns:
        A search response holding ``hits``.

    """
    return {"hits": {"hits": hits}}


def _client(indices: dict[str, dict[str, list[dict[str, object]]]]) -> MagicMock:
    """Return a mocked client answering by index and query kind (sample, match, knn).

    Args:
        indices: Each index's replies.

    Returns:
        A mocked client answering by index and query kind (sample, match, knn).

    """

    async def search(*, index: str, body: dict[str, object], params: object) -> object:
        del params
        await asyncio.sleep(0)
        query = cast("dict[str, object]", body["query"])
        kind = (
            "sample"
            if "function_score" in query
            else "knn"
            if "knn" in query
            else "match"
        )
        return _reply(indices.get(index, {}).get(kind, []))

    client = MagicMock()
    client.search = AsyncMock(side_effect=search)
    return client


@pytest.mark.asyncio
async def test_probe_index_scores_a_healthy_and_a_broken_swap() -> None:
    """The probe scores a new index that serves what the previous one did.

    And one that does not.

    Why this test is important: The probe is the evidence a sink flip or reindex is kept
        or swapped back on; a probe that scored a broken index healthy would let an org
        lose its search silently.

    What it tests:
        - A new index that finds every sampled document, overlaps the old results and
          finds its own chunks by vector scores 1.0 on all three, with one query per
          sample.
        - A new index that finds none of the old documents scores 0 known-answer recall
          and 0 overlap; a new index whose vectors find other documents scores 0
          self-recall.
        - Hits from several domains are merged by score, so the best one decides.

    """
    samples = [_hit("d1", 1.0), _hit("d2", 1.0)]
    healthy = _client({
        _OLD: {"sample": samples, "match": [_hit("d1", 2.0), _hit("d2", 1.0)]},
        _NEW: {
            "sample": [_hit("d1", 1.0, vector=True)],
            "match": [_hit("d2", 3.0), _hit("d1", 2.0)],
            "knn": [_hit("d1", 0.9)],
        },
    })
    clients = cast("dict[str, AsyncOpenSearch]", {"a": healthy})
    probe = await probe_index(clients, _ORG, _NEW, _OLD, options=ProbeIndexOptions())
    assert probe == IndexProbe(
        samples=2, known_answer_recall=1.0, overlap=1.0, self_recall=1.0
    )

    broken = _client({
        _OLD: {"sample": samples, "match": [_hit("d1", 2.0)]},
        _NEW: {
            "sample": [_hit("d1", 1.0, vector=True)],
            "match": [_hit("x", 1.0)],
            "knn": [_hit("x", 0.9)],
        },
    })
    clients = cast("dict[str, AsyncOpenSearch]", {"a": broken})
    probe = await probe_index(
        clients, _ORG, _NEW, _OLD, options=ProbeIndexOptions(settings=ProbeSettings(k=1))
    )
    assert probe == IndexProbe(
        samples=2, known_answer_recall=0.0, overlap=0.0, self_recall=0.0
    )

    far = _client({_NEW: {"match": [_hit("x", 1.0)], "knn": [_hit("x", 0.1)]}})
    near = _client({
        _NEW: {
            "sample": [_hit("d1", 1.0, vector=True)],
            "match": [_hit("d1", 5.0)],
            "knn": [_hit("d1", 0.9)],
        },
        _OLD: {"sample": [_hit("d1", 1.0)]},
    })
    clients = cast("dict[str, AsyncOpenSearch]", {"far": far, "near": near})
    probe = await probe_index(
        clients, _ORG, _NEW, _OLD, options=ProbeIndexOptions(settings=ProbeSettings(k=1))
    )
    assert (probe.known_answer_recall, probe.self_recall) == (1.0, 1.0)


@pytest.mark.asyncio
async def test_probe_index_without_previous_or_chunks() -> None:
    """With no previous index the probe samples the new one; with no chunks it scores 1.0.

    Why this test is important: A sink flip into a shared index has no previous index to
        compare with, and a new org has nothing to lose; neither may read as a failure
        that swaps the org back.

    What it tests:
        - With no previous index, the lexical samples come from the new index and overlap
          is 1.0.
        - An org with no chunks scores 1.0 everywhere with zero samples.
        - The sampling query filters on the org and is seeded, and a long text is cut to
          ``query_words`` words.

    """
    long_text = " ".join(f"w{i}" for i in range(100))
    new = _client({
        _NEW: {
            "sample": [
                {"_score": 1.0, "_source": {"document_id": "d1", "text": long_text}}
            ],
            "match": [_hit("d1", 1.0)],
        }
    })
    clients = cast("dict[str, AsyncOpenSearch]", {"a": new})
    settings = ProbeSettings(query_words=5, seed=7)
    probe = await probe_index(
        clients, _ORG, _NEW, options=ProbeIndexOptions(settings=settings)
    )
    assert probe == IndexProbe(
        samples=1, known_answer_recall=1.0, overlap=1.0, self_recall=1.0
    )
    bodies = [
        cast("dict[str, object]", call.kwargs)["body"]
        for call in cast("list[object]", cast("AsyncMock", new.search).await_args_list)
    ]
    sample = cast("dict[str, object]", dig(bodies, 0, "query", "function_score"))
    assert dig(sample, "random_score", "seed") == _EXPECTED_SAMPLE
    assert dig(sample, "query", "bool", "filter") == [{"term": {"org_id": _ORG}}]
    match = cast(
        "dict[str, object]",
        next(b for b in bodies if "bool" in cast("dict[str, object]", dig(b, "query"))),
    )
    assert dig(match, "query", "bool", "must") == [{"match": {"text": "w0 w1 w2 w3 w4"}}]

    empty = cast("dict[str, AsyncOpenSearch]", {"a": _client({})})
    probe = await probe_index(empty, _ORG, _NEW, _OLD, options=ProbeIndexOptions())
    assert probe == IndexProbe(
        samples=0, known_answer_recall=1.0, overlap=1.0, self_recall=1.0
    )
