"""Tests for the Bedrock embedding provider and its ``bedrock`` kind.

In the embedding factory.

The provider takes an injected bedrock-runtime invoker (the consumer-side ``InvokeModel``
seam), so these tests mock it and assert the provider's own logic: the Titan v2 request
body, the response parse, the dimension check, and the botocore error mapping.
"""

from __future__ import annotations

import io
import json
from unittest.mock import AsyncMock, MagicMock

import pytest
from botocore.exceptions import ClientError, EndpointConnectionError  # type: ignore[import-untyped]

from techai_webutils.clients.embedding.bedrock import BedrockEmbeddingProvider
from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode

_MODEL = "amazon.titan-embed-text-v2:0"


def _invoker_returning(*vectors: list[float], tokens: int = 4) -> MagicMock:
    """Return a mock invoker whose ``invoke_model`` yields one Titan v2 response per call.

    In order.
    """
    responses = [
        {
            "body": io.BytesIO(
                json.dumps({"embedding": v, "inputTextTokenCount": tokens}).encode()
            )
        }
        for v in vectors
    ]
    invoker = MagicMock()
    invoker.invoke_model = AsyncMock(side_effect=responses)
    return invoker


def _client_error(code: str, status: int) -> ClientError:
    """Build a botocore ``ClientError`` carrying ``code`` and HTTP ``status``."""
    return ClientError(
        {
            "Error": {"Code": code, "Message": "x"},
            "ResponseMetadata": {
                "RequestId": "",
                "HostId": "",
                "HTTPStatusCode": status,
                "HTTPHeaders": {},
                "RetryAttempts": 0,
            },
        },
        "InvokeModel",
    )


class TestBedrockEmbeddingProvider:
    """Tests for ``BedrockEmbeddingProvider``."""

    @pytest.mark.asyncio
    async def test_embed_sends_titan_v2_body_and_returns_vector(self) -> None:
        """embed() calls InvokeModel with the Titan v2 body.

        And returns the vector plus its token count.

        Why this test is important:
          - A semantic cache embeds every query through this call; a wrong body (missing
            ``dimensions`` or ``normalize``) yields vectors the cache index can't compare.

        What it tests:
          - The request names the model, sends JSON ``{inputText, dimensions,
            normalize}``, and the result carries the embedding, the model id, and
            ``inputTextTokenCount``.
        """
        invoker = _invoker_returning([0.1, 0.2, 0.3], tokens=7)
        provider = BedrockEmbeddingProvider(invoker, model=_MODEL, dimension=3)

        result = await provider.embed("hello")

        assert result.embedding == [0.1, 0.2, 0.3]
        assert result.model == _MODEL
        assert result.token_count == 7
        kwargs = invoker.invoke_model.call_args.kwargs
        assert kwargs["modelId"] == _MODEL
        assert kwargs["contentType"] == "application/json"
        assert json.loads(kwargs["body"]) == {
            "inputText": "hello",
            "dimensions": 3,
            "normalize": True,
        }

    @pytest.mark.asyncio
    async def test_embed_batch_calls_once_per_text_in_order(self) -> None:
        """embed_batch() invokes the model once per text and keeps the input order.

        Why this test is important:
          - Titan v2 has no batch body, so the provider must fan out; results out of order
            would bind a cached answer to the wrong question.

        What it tests:
          - Two texts → two InvokeModel calls, results in input order.
        """
        invoker = _invoker_returning([1.0], [2.0])
        provider = BedrockEmbeddingProvider(
            invoker, model=_MODEL, dimension=1, normalize=False
        )

        results = await provider.embed_batch(["a", "b"])

        assert [r.embedding for r in results] == [[1.0], [2.0]]
        assert invoker.invoke_model.await_count == 2
        bodies = [
            json.loads(c.kwargs["body"]) for c in invoker.invoke_model.call_args_list
        ]
        assert [b["inputText"] for b in bodies] == ["a", "b"]
        assert all(b["normalize"] is False for b in bodies)

    @pytest.mark.asyncio
    async def test_embed_rejects_dimension_mismatch(self) -> None:
        """A vector whose length differs.

        From the configured dimension is an INTERNAL error.

        Why this test is important:
          - A model/config mismatch otherwise surfaces as a cache index that never
            matches.

        What it tests:
          - A 2-d vector against dimension=3 raises ``AppError`` with
            ``ErrorCode.INTERNAL``.
        """
        provider = BedrockEmbeddingProvider(
            _invoker_returning([0.1, 0.2]), model=_MODEL, dimension=3
        )

        with pytest.raises(AppError) as exc:
            await provider.embed("hello")

        assert exc.value.code is ErrorCode.INTERNAL

    @pytest.mark.parametrize(
        ("error", "expected"),
        [
            (_client_error("ThrottlingException", 429), ErrorCode.UNAVAILABLE),
            (_client_error("AccessDeniedException", 403), ErrorCode.INTERNAL),
            (
                EndpointConnectionError(endpoint_url="https://bedrock"),
                ErrorCode.UNAVAILABLE,
            ),
        ],
    )
    @pytest.mark.asyncio
    async def test_embed_maps_botocore_errors_to_coded_errors(
        self, error: Exception, expected: ErrorCode
    ) -> None:
        """A botocore failure surfaces as a coded ``AppError``, never a raw SDK exception.

        Why this test is important:
          - The retry and circuit-breaker decorators act on the error code; a raw
            ``ClientError`` would neither retry on throttling nor stop on access-denied.

        What it tests:
          - Throttling and connection errors → UNAVAILABLE; access-denied → INTERNAL.
        """
        invoker = MagicMock()
        invoker.invoke_model = AsyncMock(side_effect=error)
        provider = BedrockEmbeddingProvider(invoker, model=_MODEL, dimension=3)

        with pytest.raises(AppError) as exc:
            await provider.embed("hello")

        assert exc.value.code is expected

    def test_reports_model_and_dimension(self) -> None:
        """dimension() and model_name() return the configured values.

        What it tests:
          - The cache index is sized from ``dimension()``; ``model_name()`` labels its
            facts.
        """
        provider = BedrockEmbeddingProvider(MagicMock(), model=_MODEL, dimension=1024)

        assert provider.dimension() == 1024
        assert provider.model_name() == _MODEL


class TestBedrockEmbeddingKind:
    """Tests for the ``bedrock`` kind in ``new_embedding_from_config``."""

    def test_factory_builds_bedrock_provider(self) -> None:
        """The ``bedrock`` kind builds a ``BedrockEmbeddingProvider``.

        With the configured model.

        Why this test is important:
          - A deployment selects the Bedrock embedder by config alone; the factory must
            honour the kind.

        What it tests:
          - kind=bedrock → a ``BedrockEmbeddingProvider`` reporting the configured model
            and dimension, with no network call at construction.
        """
        provider = new_embedding_from_config(
            EmbeddingConfig(
                kind=EmbeddingKind.BEDROCK,
                model=_MODEL,
                dimension=1024,
                region="us-east-1",
            )
        )

        assert isinstance(provider, BedrockEmbeddingProvider)
        assert provider.model_name() == _MODEL
        assert provider.dimension() == 1024
