"""Unit tests for ``CoalescingEmbedding``, which packs concurrent callers into shared batches."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.embedding.coalesce import CoalescingEmbedding
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult


def _inner() -> MagicMock:
    """Return a mocked provider whose vector for a text is ``[len(text)]``."""
    inner = MagicMock(spec=EmbeddingProvider)

    def batch(
        texts: list[str], *, input_type: str = "document"
    ) -> list[EmbeddingResult]:
        del input_type
        return [EmbeddingResult([float(len(t))], "m", 1) for t in texts]

    inner.embed_batch = AsyncMock(side_effect=batch)
    inner.model_name.return_value = "m"
    inner.dimension.return_value = 1
    return inner


@pytest.mark.asyncio
async def test_concurrent_callers_share_full_batches() -> None:
    """Test that concurrent callers' texts go to the provider in batches of ``max_batch``.

    **Why this test is important:**
      - An embed stage handles many small documents at once; one provider call per document
        pays a call's overhead and quota per few texts instead of per full batch.

    **What it tests:**
      - three callers with 2, 3 and 1 texts (``max_batch`` 3) make two provider calls of 3
      - each caller gets its own texts' vectors, in order
      - the decorator reports the inner provider's model and dimension
    """
    inner = _inner()
    coalescer = CoalescingEmbedding(inner, max_batch=3, linger_seconds=10)

    results = await asyncio.gather(
        coalescer.embed_batch(["a", "bb"]),
        coalescer.embed_batch(["ccc", "dddd", "eeeee"]),
        coalescer.embed("ffffff"),
    )

    sizes = [len(call.args[0]) for call in inner.embed_batch.await_args_list]
    assert sizes == [3, 3]
    assert [r.embedding for r in results[0]] == [[1.0], [2.0]]
    assert [r.embedding for r in results[1]] == [[3.0], [4.0], [5.0]]
    assert results[2].embedding == [6.0]
    assert (coalescer.model_name(), coalescer.dimension()) == ("m", 1)


@pytest.mark.asyncio
async def test_partial_batch_goes_after_linger_and_input_types_stay_apart() -> None:
    """Test that a partial batch is sent after the linger, one batch per input type.

    **Why this test is important:**
      - A lone document must not wait for a batch that never fills, and a query vector and a
        document vector come from different prompts: mixing them in one call embeds one wrong.

    **What it tests:**
      - one document text and one query text (``max_batch`` 8) each go in their own call,
        tagged with their own input type
      - an empty batch returns no results and calls nothing
    """
    inner = _inner()
    coalescer = CoalescingEmbedding(inner, max_batch=8, linger_seconds=0.01)

    doc, query = await asyncio.gather(
        coalescer.embed("doc"), coalescer.embed("q", input_type="query")
    )

    types = sorted(
        call.kwargs["input_type"] for call in inner.embed_batch.await_args_list
    )
    assert types == ["document", "query"]
    assert (doc.embedding, query.embedding) == ([3.0], [1.0])
    assert await coalescer.embed_batch([]) == []


@pytest.mark.asyncio
async def test_failed_batch_fails_every_caller_in_it() -> None:
    """Test that a provider error reaches every caller whose text was in the failed batch.

    **Why this test is important:**
      - A caller left waiting on a batch that failed would hang its message until the
        deadline; each caller must see the error to retry its own texts.

    **What it tests:**
      - two callers in one failed batch both raise the provider's error
      - an invalid ``max_batch`` or ``linger_seconds`` is refused at construction
    """
    inner = _inner()
    inner.embed_batch.side_effect = RuntimeError("throttled")
    coalescer = CoalescingEmbedding(inner, max_batch=2, linger_seconds=10)

    outcomes = await asyncio.gather(
        coalescer.embed("a"), coalescer.embed("b"), return_exceptions=True
    )

    assert all(isinstance(o, RuntimeError) for o in outcomes)
    with pytest.raises(AppValueError):
        CoalescingEmbedding(inner, max_batch=0, linger_seconds=1)
    with pytest.raises(AppValueError):
        CoalescingEmbedding(inner, max_batch=1, linger_seconds=-1)
