"""Unit tests for ``MemoizedEmbedding``, the per-request query-embedding memo."""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.embedding.memo import MemoizedEmbedding, reset_embedding_memo
from techai_webutils.clients.embedding.stub import StubEmbeddingProvider


@pytest.mark.asyncio
async def test_embedding_classifier_embeds_query_once_per_request() -> None:
    """Test that two consumers in one request embed the query once.

    **Why this test is important:**
      - The router and the semantic cache both need the query's vector; embedding it twice
        doubles a model call on every request.

    **What it tests:**
      - two ``embed`` calls for the same text in one context call the inner provider once
        and return equal results
      - after ``reset_embedding_memo()`` (a new request) the inner provider is called
        again
      - another task (another request) does not see this request's memo
      - the memo reports the inner provider's model and dimension
    """
    inner = StubEmbeddingProvider("stub-embed", 4)
    inner.embed = AsyncMock(wraps=inner.embed)  # type: ignore[method-assign]
    memo = MemoizedEmbedding(inner)

    async def request() -> None:
        reset_embedding_memo()
        await memo.embed("q")

    reset_embedding_memo()
    first, second = await memo.embed("q"), await memo.embed("q")
    await asyncio.create_task(request())
    await memo.embed("q")
    reset_embedding_memo()
    await memo.embed("q")

    assert first == second
    assert inner.embed.await_count == 3
    assert (memo.model_name(), memo.dimension()) == ("stub-embed", 4)
    assert await memo.embed_batch(["a", "b"]) == await inner.embed_batch(["a", "b"])


@pytest.mark.asyncio
async def test_memo_never_shares_a_vector_across_models() -> None:
    """Two memoized embedders over different models embed the same text.

    Separately.

    **Why this test is important:**
      - The cache and the classifier can embed one query with different models; a shared
        vector puts a foreign-space (or wrong-dimension) vector into a KNN or a
        classifier.

    **What it tests:**
      - in one request, each embedder's inner provider is called and each result names its
        model
    """
    small, large = StubEmbeddingProvider("small", 4), StubEmbeddingProvider("large", 8)
    reset_embedding_memo()

    first = await MemoizedEmbedding(small).embed("q")
    second = await MemoizedEmbedding(large).embed("q")

    assert (first.model, len(first.embedding)) == ("small", 4)
    assert (second.model, len(second.embedding)) == ("large", 8)
