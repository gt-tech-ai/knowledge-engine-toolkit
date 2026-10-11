"""Tests for the Ollama embedding provider + the env-aware embedding factory.

Prompt/network-agnostic: the provider takes an injected httpx client, so these tests mock it and
assert the provider's logic (the /api/embed request shape + the plural-key response parse + token_count=0).
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.ollama import OllamaEmbeddingProvider


def _client_returning(embeddings: list[list[float]]) -> httpx.AsyncClient:
    """Return a mock httpx.AsyncClient whose post() returns an Ollama /api/embed response."""
    resp = MagicMock()
    resp.json.return_value = {"embeddings": embeddings}
    resp.raise_for_status.return_value = None
    client = MagicMock(spec=httpx.AsyncClient)
    client.post = AsyncMock(return_value=resp)
    return client


class TestOllamaEmbeddingProvider:
    """Tests for the Ollama embedding provider."""

    @pytest.mark.asyncio
    async def test_embed_posts_api_embed_and_returns_first_vector(self) -> None:
        """embed() POSTs /api/embed {model, input:[text]} and returns the first embedding, token_count=0.

        Why this test is important:
          - The whole vector path depends on the request shape (input list) + the plural `embeddings`
            response key; a mismatch silently breaks retrieval.

        What it tests:
          - embed("hello") posts input=["hello"] and returns EmbeddingResult(vector, model, token_count=0).
        """
        client = _client_returning([[0.1, 0.2, 0.3]])
        provider = OllamaEmbeddingProvider(client, model="nomic-embed-text", dimension=3)

        result = await provider.embed("hello")

        assert result.embedding == [0.1, 0.2, 0.3]
        assert result.model == "nomic-embed-text"
        assert result.token_count == 0
        _, kwargs = client.post.call_args
        assert kwargs["json"] == {"model": "nomic-embed-text", "input": ["hello"]}
        assert client.post.call_args.args[0] == "/api/embed"

    @pytest.mark.asyncio
    async def test_embed_batch_is_one_call(self) -> None:
        """embed_batch sends ONE /api/embed call for all texts (native batching), one result per vector.

        What it tests:
          - embed_batch(["a","b"]) → one post, two EmbeddingResults matching the response order.
        """
        client = _client_returning([[1.0], [2.0]])
        provider = OllamaEmbeddingProvider(client, model="m", dimension=1)

        results = await provider.embed_batch(["a", "b"])

        assert [r.embedding for r in results] == [[1.0], [2.0]]
        client.post.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_embed_batch_rejects_dimension_mismatch(self) -> None:
        """embed_batch fails loudly when a returned vector's length != the configured dimension.

        Why this test is important:
          - A model/config dimension mismatch otherwise surfaces far downstream as an empty search (the
            query and stored vectors have different lengths); failing at the embed boundary names the cause.

        What it tests:
          - A provider with dimension=3 receiving a 2-d vector raises ValueError mentioning the dimensions.
        """
        provider = OllamaEmbeddingProvider(
            _client_returning([[0.1, 0.2]]), model="m", dimension=3
        )
        with pytest.raises(ValueError, match="expected 3"):
            await provider.embed_batch(["a"])

    @pytest.mark.asyncio
    async def test_embed_batch_rejects_missing_embeddings_key(self) -> None:
        """embed_batch fails loudly (clear message) when the response lacks the 'embeddings' key.

        What it tests:
          - A response of {"data": ...} (wrong shape) raises ValueError naming the missing key rather
            than a bare KeyError.
        """
        resp = MagicMock()
        resp.json.return_value = {"data": []}
        resp.raise_for_status.return_value = None
        client = MagicMock(spec=httpx.AsyncClient)
        client.post = AsyncMock(return_value=resp)
        provider = OllamaEmbeddingProvider(client, model="m", dimension=1)
        with pytest.raises(ValueError, match="missing 'embeddings'"):
            await provider.embed_batch(["a"])

    def test_dimension_and_model_name(self) -> None:
        """dimension()/model_name() report the configured values."""
        provider = OllamaEmbeddingProvider(
            MagicMock(spec=httpx.AsyncClient), model="m", dimension=768
        )
        assert provider.dimension() == 768
        assert provider.model_name() == "m"


class TestEmbeddingFactory:
    """Tests for the embedding factory."""

    def test_from_config_builds_ollama(self) -> None:
        """new_embedding_from_config(kind=ollama) builds an OllamaEmbeddingProvider from config."""
        provider = new_embedding_from_config(
            EmbeddingConfig(
                kind=EmbeddingKind.OLLAMA,
                host="http://ollama:11434",
                model="nomic-embed-text",
                dimension=768,
            ),
        )
        assert isinstance(provider, OllamaEmbeddingProvider)
        assert provider.model_name() == "nomic-embed-text"
        assert provider.dimension() == 768

    def test_from_config_wires_the_configured_timeout(self) -> None:
        """The factory sets config.timeout_seconds on the httpx client (not httpx's 5s default).

        **Why this test is important:**
          - A CPU-only Ollama embed of a sub-batch takes tens of seconds; httpx's 5s default aborts
            every real embed with a ReadTimeout, stranding the document in the vector-dev path. The
            factory must override it, so a regression back to the default is a silent re-break.

        **What it tests:**
          - A provider built with timeout_seconds=123.0 opens its httpx client with a 123.0s timeout.
        """
        with patch("httpx.AsyncClient") as client_cls:
            provider = new_embedding_from_config(
                EmbeddingConfig(kind=EmbeddingKind.OLLAMA, timeout_seconds=123.0),
            )
        assert isinstance(provider, OllamaEmbeddingProvider)
        assert client_cls.call_args.kwargs["timeout"] == pytest.approx(123.0)

    def test_unknown_kind_raises(self) -> None:
        """An unknown embedding kind fails loudly."""
        with pytest.raises(ValueError, match="unknown embedding kind"):
            new_embedding_from_config(EmbeddingConfig(kind="bogus"))  # type: ignore[arg-type]
