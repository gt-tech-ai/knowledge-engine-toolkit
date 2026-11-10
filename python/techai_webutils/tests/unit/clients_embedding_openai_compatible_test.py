"""Unit tests for the OpenAI-compatible embedding provider.

And the query/document prefixes.
"""

from __future__ import annotations

import json
from typing import TYPE_CHECKING, cast
from unittest.mock import patch

import httpx
import pytest

from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.openai_compatible import (
    OpenAICompatibleEmbeddingProvider,
)
from techai_webutils.clients.embedding.prefix import PrefixedEmbedding
from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable

_EXPECTED_VALUE = 7

_DIMENSION = 3


def _provider(
    handler: Callable[[httpx.Request], httpx.Response],
) -> OpenAICompatibleEmbeddingProvider:
    """Return a provider whose server is ``handler``.

    Returns:
        A provider whose server is ``handler``.

    """
    client = httpx.AsyncClient(
        base_url="http://embedder:8000", transport=httpx.MockTransport(handler)
    )
    return OpenAICompatibleEmbeddingProvider(client, model="e5", dimension=_DIMENSION)


def _vector(i: int) -> list[float]:
    """Return a distinct 3-d vector for input ``i``.

    Returns:
        A distinct 3-d vector for input ``i``.

    """
    return [float(i), 0.5, 0.25]


def _echo(sent: list[dict[str, object]]) -> Callable[[httpx.Request], httpx.Response]:
    """Return a server that records each body and embeds every input.

    Answering out of order.

    Returns:
        A server that records each body and embeds every input.

    """

    def handler(request: httpx.Request) -> httpx.Response:
        body = cast("dict[str, object]", json.loads(request.content))
        sent.append(body)
        data = [
            {"index": i, "embedding": _vector(i)}
            for i in range(len(cast("list[object]", body["input"])))
        ]
        return httpx.Response(
            200, json={"data": data[::-1], "usage": {"prompt_tokens": 7}}
        )

    return handler


@pytest.mark.asyncio
async def test_batch_maps_to_the_v1_embeddings_shape() -> None:
    """Test that one batch is one ``/v1/embeddings`` call, returned in input order.

    **Why this test is important:**
      - vLLM, TEI and LiteLLM all serve this shape; a wrong path or body is a 404 or 422
        on the first embed of a self-hosted deployment.

    **What it tests:**
      - three texts are posted once to ``/v1/embeddings`` as ``{"model", "input"}``
      - the vectors come back in input order even though the server answers out of order
      - the batch's ``prompt_tokens`` is reported once, so the batch sums to 7
      - an empty batch makes no call
    """
    sent: list[dict[str, object]] = []
    paths: list[str] = []

    def handler(request: httpx.Request) -> httpx.Response:
        paths.append(request.url.path)
        return _echo(sent)(request)

    provider = _provider(handler)

    results = await provider.embed_batch(["a", "b", "c"])

    assert paths == ["/v1/embeddings"]
    assert sent == [{"model": "e5", "input": ["a", "b", "c"]}]
    assert [r.embedding for r in results] == [_vector(0), _vector(1), _vector(2)]
    assert sum(r.token_count for r in results) == _EXPECTED_VALUE
    assert await provider.embed_batch([]) == []
    assert len(paths) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("status", "code"),
    [
        (429, ErrorCode.UNAVAILABLE),
        (503, ErrorCode.UNAVAILABLE),
        (400, ErrorCode.INTERNAL),
    ],
)
async def test_status_errors_are_coded(status: int, code: ErrorCode) -> None:
    """Test that 429 and 503 raise transient codes and a 400 a terminal one.

    **Why this test is important:**
      - The retry decorator retries only transient codes: a throttled embedder must be
        retried, a bad request must not be.

    **What it tests:**
      - 429 and 503 → ``UNAVAILABLE`` (transient); 400 → ``INTERNAL``
    """
    provider = _provider(lambda _request: httpx.Response(status, json={}))

    with pytest.raises(AppError) as raised:
        _ = await provider.embed("a")

    assert raised.value.code is code
    assert raised.value.is_transient is (code is ErrorCode.UNAVAILABLE)


@pytest.mark.asyncio
async def test_timeout_and_bad_vectors_are_coded() -> None:
    """Test that a timeout is ``TIMEOUT`` and a wrong-size.

    Or missing vector is ``INTERNAL``.

    **Why this test is important:**
      - A vector of the wrong size would be rejected far away, at the store; a model or
        config mismatch must fail at the embedder with a clear message.

    **What it tests:**
      - a ``ReadTimeout`` → ``TIMEOUT``; a 2-d vector for a 3-d config → ``INTERNAL``; one
        vector for two texts → ``INTERNAL``
    """

    def timeout(request: httpx.Request) -> httpx.Response:
        msg = "slow"
        raise httpx.ReadTimeout(msg, request=request)

    short = {"data": [{"index": 0, "embedding": [1.0, 2.0]}]}
    missing = {"data": [{"index": 0, "embedding": _vector(0)}]}

    with pytest.raises(AppError) as timed_out:
        _ = await _provider(timeout).embed("a")
    with pytest.raises(AppError, match="2-d vector") as wrong_size:
        _ = await _provider(lambda _r: httpx.Response(200, json=short)).embed("a")
    with pytest.raises(AppError, match="1 vectors for 2 texts") as too_few:
        _ = await _provider(lambda _r: httpx.Response(200, json=missing)).embed_batch([
            "a",
            "b",
        ])

    assert timed_out.value.code is ErrorCode.TIMEOUT
    assert wrong_size.value.code is ErrorCode.INTERNAL
    assert too_few.value.code is ErrorCode.INTERNAL


def test_factory_applies_the_timeout_and_the_bearer_key() -> None:
    """Test that the ``openai_compatible`` kind builds a client.

    With the configured timeout and key.

    **Why this test is important:**
      - httpx's 5 s default aborts a large batch on a busy server; a hosted server rejects
        a call without its key.

    **What it tests:**
      - the httpx client is built on ``host`` with ``timeout_seconds`` and an
        ``Authorization: Bearer`` header; without a key no header is sent; the key is not
        in the config's repr
    """
    config = EmbeddingConfig(
        kind=EmbeddingKind.OPENAI_COMPATIBLE,
        host="http://embedder:8000",
        model="e5",
        dimension=_DIMENSION,
        timeout_seconds=12.5,
        api_key="sk-test",
    )

    with patch("techai_webutils.clients.embedding.builder.httpx.AsyncClient") as client:
        provider = new_embedding_from_config(config)
        _ = new_embedding_from_config(
            EmbeddingConfig(kind=EmbeddingKind.OPENAI_COMPATIBLE)
        )

    assert isinstance(provider, OpenAICompatibleEmbeddingProvider)
    keyed, bare = client.call_args_list
    assert keyed.kwargs == {
        "base_url": "http://embedder:8000",
        "timeout": 12.5,
        "headers": {"Authorization": "Bearer sk-test"},
    }
    assert bare.kwargs["headers"] == {}
    assert "sk-test" not in repr(config)


@pytest.mark.asyncio
async def test_query_and_document_prefixes_reach_the_request() -> None:
    """Test that a configured prefix reaches the server per input type.

    And none is the default.

    **Why this test is important:**
      - nomic-embed-text and E5 rank worse without their task prefixes; a consumer that
        never sets one must keep today's vectors, or its existing index stops matching its
        queries.

    **What it tests:**
      - with E5's prefixes, a query is sent as ``query: …`` and a document batch as
        ``passage: …``
      - ``embed`` with no ``input_type`` embeds as a document
      - with no prefix the factory returns the bare provider and the text is sent
        unchanged
    """
    sent: list[dict[str, object]] = []
    inner = _provider(_echo(sent))
    prefixed = PrefixedEmbedding(
        inner, query_prefix="query: ", document_prefix="passage: "
    )

    _ = await prefixed.embed("pump", input_type="query")
    _ = await prefixed.embed_batch(["valve"], input_type="document")
    _ = await prefixed.embed("seal")
    _ = await inner.embed("plain", input_type="query")

    assert [body["input"] for body in sent] == [
        ["query: pump"],
        ["passage: valve"],
        ["passage: seal"],
        ["plain"],
    ]
    assert (prefixed.dimension(), prefixed.model_name()) == (_DIMENSION, "e5")
    assert not isinstance(
        new_embedding_from_config(EmbeddingConfig(kind=EmbeddingKind.STUB)),
        PrefixedEmbedding,
    )
    assert isinstance(
        new_embedding_from_config(
            EmbeddingConfig(kind=EmbeddingKind.STUB, query_prefix="search_query: ")
        ),
        PrefixedEmbedding,
    )
