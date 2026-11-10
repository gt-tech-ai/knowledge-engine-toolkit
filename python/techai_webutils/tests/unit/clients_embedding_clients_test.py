"""Tests for the Ollama embedding provider + the env-aware embedding factory.

Prompt/network-agnostic: the provider takes an injected httpx client, so these tests mock
it and assert the provider's logic (the /api/embed request shape + the plural-key response
parse + token_count=0).
"""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock, patch

import httpx
import pytest

from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.ollama import OllamaEmbeddingProvider
from techai_webutils.core.errors import AppValueError

_EXPECTED_DIMENSION = 768


def _client_returning(embeddings: list[list[float]]) -> httpx.AsyncClient:
    """Return a mock httpx.AsyncClient whose post() returns an Ollama /api/embed response.

    Args:
        embeddings: The vectors the server returns.

    Returns:
        A mock httpx.AsyncClient whose post() returns an Ollama /api/embed.

    """
    resp = MagicMock()
    cast("MagicMock", resp.json).return_value = {"embeddings": embeddings}
    cast("MagicMock", resp.raise_for_status).return_value = None
    client = MagicMock(spec=httpx.AsyncClient)
    client.post = AsyncMock(return_value=resp)
    return client


class TestOllamaEmbeddingProvider:
    """Tests for the Ollama embedding provider."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_embed_posts_api_embed_and_returns_first_vector() -> None:
        """embed() POSTs /api/embed {model, input:[text]}.

        It returns the first embedding, with token_count=0.

        Why this test is important:
          - The whole vector path depends on the request shape (input list) + the plural
            `embeddings` response key; a mismatch silently breaks retrieval.

        What it tests:
          - embed("hello") posts input=["hello"] and returns EmbeddingResult(vector,
            model, token_count=0).
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

    @staticmethod
    @pytest.mark.asyncio
    async def test_embed_batch_is_one_call() -> None:
        """embed_batch sends ONE /api/embed call for all texts.

        Native batching: one result per vector.

        What it tests:
          - embed_batch(["a","b"]) → one post, two EmbeddingResults matching the response
            order.
        """
        client = _client_returning([[1.0], [2.0]])
        provider = OllamaEmbeddingProvider(client, model="m", dimension=1)

        results = await provider.embed_batch(["a", "b"])

        assert [r.embedding for r in results] == [[1.0], [2.0]]
        client.post.assert_awaited_once()

    @staticmethod
    @pytest.mark.asyncio
    async def test_embed_batch_rejects_dimension_mismatch() -> None:
        """embed_batch fails loudly on a vector of the wrong length.

        The length must equal the configured dimension.

        Why this test is important:
          - A model/config dimension mismatch otherwise surfaces far downstream as an
            empty search (the query and stored vectors have different lengths); failing at
            the embed boundary names the cause.

        What it tests:
          - A provider with dimension=3 receiving a 2-d vector raises ValueError
            mentioning the dimensions.
        """
        provider = OllamaEmbeddingProvider(
            _client_returning([[0.1, 0.2]]), model="m", dimension=3
        )
        with pytest.raises(AppValueError, match="expected 3"):
            _ = await provider.embed_batch(["a"])

    @staticmethod
    @pytest.mark.asyncio
    async def test_embed_batch_rejects_missing_embeddings_key() -> None:
        """embed_batch fails loudly when the response lacks 'embeddings'.

        The message says so clearly.

        What it tests:
          - A response of {"data": ...} (wrong shape) raises ValueError naming the missing
            key rather than a bare KeyError.
        """
        resp = MagicMock()
        cast("MagicMock", resp.json).return_value = {"data": []}
        cast("MagicMock", resp.raise_for_status).return_value = None
        client = MagicMock(spec=httpx.AsyncClient)
        client.post = AsyncMock(return_value=resp)
        provider = OllamaEmbeddingProvider(client, model="m", dimension=1)
        with pytest.raises(AppValueError, match="missing 'embeddings'"):
            _ = await provider.embed_batch(["a"])

    @staticmethod
    def test_dimension_and_model_name() -> None:
        """dimension()/model_name() report the configured values."""
        provider = OllamaEmbeddingProvider(
            MagicMock(spec=httpx.AsyncClient), model="m", dimension=768
        )
        assert provider.dimension() == _EXPECTED_DIMENSION
        assert provider.model_name() == "m"


class TestEmbeddingFactory:
    """Tests for the embedding factory."""

    @staticmethod
    def test_from_config_builds_ollama() -> None:
        """new_embedding_from_config(kind=ollama) builds an OllamaEmbeddingProvider.

        From the config.
        """
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
        assert provider.dimension() == _EXPECTED_DIMENSION

    @staticmethod
    def test_from_config_wires_the_configured_timeout() -> None:
        """The factory sets config.timeout_seconds on the httpx client.

        Not httpx's 5s default.

        **Why this test is important:**
          - A CPU-only Ollama embed of a sub-batch takes tens of seconds; httpx's 5s
            default aborts every real embed with a ReadTimeout, stranding the document in
            the vector-dev path. The factory must override it, so a regression back to the
            default is a silent re-break.

        **What it tests:**
          - A provider built with timeout_seconds=123.0 opens its httpx client with a
            123.0s timeout.
        """
        with patch("httpx.AsyncClient") as client_cls:
            provider = new_embedding_from_config(
                EmbeddingConfig(kind=EmbeddingKind.OLLAMA, timeout_seconds=123.0),
            )
        assert isinstance(provider, OllamaEmbeddingProvider)
        assert client_cls.call_args.kwargs["timeout"] == pytest.approx(123.0)

    @staticmethod
    def test_unknown_kind_raises() -> None:
        """An unknown embedding kind fails loudly."""
        with pytest.raises(AppValueError, match="unknown embedding kind"):
            _ = new_embedding_from_config(
                EmbeddingConfig(kind=cast("EmbeddingKind", cast("object", "bogus")))
            )
