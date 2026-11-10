"""Unit tests for the ``embedding`` route classifier over controlled exemplar vectors."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.model_gateway.routed.classifier import (
    route_classifier_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.core.interfaces.model_gateway import RouteContext
from techai_webutils.core.types.routing import Classification, Route

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.routing import RouteClassifier

_EXPECTED_CONFIDENCE = 0.9

_VECTORS = {
    "what is x": [1.0, 0.0],
    "define y": [0.9, 0.1],
    "summarize the report": [0.0, 1.0],
    "give me a summary": [0.1, 0.9],
    "lookup-ish": [0.95, 0.05],
    "halfway": [0.7, 0.7],
}
_ROUTES = {
    "lookup": Route("lookup", "m/a", exemplars=("what is x", "define y"), threshold=0.9),
    "summarize": Route(
        "summarize",
        "m/b",
        exemplars=("summarize the report", "give me a summary"),
        threshold=0.9,
    ),
    "generate": Route("generate", "m/a"),
}


def _embedding() -> MagicMock:
    """Return a mocked embedder answering the controlled vectors.

    (queries and exemplars embed as queries).

    Returns:
        A mocked embedder answering the controlled vectors.

    """

    def result(text: str) -> EmbeddingResult:
        return EmbeddingResult(embedding=_VECTORS[text], model="m", token_count=0)

    async def embed(text: str, *, input_type: str = "document") -> EmbeddingResult:
        assert input_type == "query"
        await asyncio.sleep(0)
        return result(text)

    async def embed_batch(
        texts: list[str], *, input_type: str = "document"
    ) -> list[EmbeddingResult]:
        return [await embed(text, input_type=input_type) for text in texts]

    provider = MagicMock(spec=EmbeddingProvider)
    provider.embed = AsyncMock(side_effect=embed)
    provider.embed_batch = AsyncMock(side_effect=embed_batch)
    return provider


async def _classifier(embedding: MagicMock) -> RouteClassifier:
    """Build and warm an embedding classifier over ``_ROUTES``.

    Args:
        embedding: The embedding provider.

    Returns:
        The classifier.

    """
    classifier = route_classifier_from_config(
        {"kind": "embedding", "top_k": 2},
        route_names=frozenset(_ROUTES),
        default_route="",
        routes=_ROUTES,
        embedding=embedding,
    )
    await classifier.warm()
    return classifier


@pytest.mark.asyncio
async def test_embedding_classifier_picks_route_with_highest_exemplar_similarity() -> (
    None
):
    """Test that a query goes to the route whose exemplars it is closest to.

    **Why this test is important:**
      - This is what lets a lookup run on a cheap model and a synthesis on a strong one.

    **What it tests:**
      - ``lookup-ish`` on a ``generate`` step goes to ``lookup`` with the top-2 mean
        cosine
      - the exemplars are embedded once, in one batch, at warm-up
    """
    embedding = _embedding()
    classifier = await _classifier(embedding)

    result = await classifier.classify(RouteContext(step="generate"), "lookup-ish")

    assert result.route == "lookup"
    assert result.confidence == pytest.approx(0.99, abs=0.01)
    assert result.degraded is False
    cast("MagicMock", cast("AsyncMock", embedding.embed_batch).assert_awaited_once)()


@pytest.mark.asyncio
async def test_embedding_classifier_below_threshold_uses_default_route() -> None:
    """Test that a low-confidence classification falls back to the step's route.

    **Why this test is important:**
      - A weak match must not move a request off its step's model.

    **What it tests:**
      - ``halfway`` (≈0.76 to either route, below 0.9) on a ``generate`` step returns
        route ``generate`` carrying the best confidence found
    """
    classifier = await _classifier(_embedding())

    result = await classifier.classify(RouteContext(step="generate"), "halfway")

    assert result.route == "generate"
    assert result.confidence < _EXPECTED_CONFIDENCE
    assert result.degraded is False


@pytest.mark.asyncio
async def test_embedding_classifier_degrades_when_embedder_fails() -> None:
    """Test that an embedder outage degrades routing instead of failing the answer.

    **Why this test is important:**
      - Routing is an optimisation, not a dependency; an embedder blip must not fail a
        query.

    **What it tests:**
      - a raising ``embed`` returns the step's route with ``degraded=True``
      - a raising ``embed_batch`` at warm-up fails the boot with ``UNAVAILABLE``
      - the embedding kind without an embedder is ``INVALID_INPUT``
    """
    embedding = _embedding()
    classifier = await _classifier(embedding)
    embedding.embed = AsyncMock(side_effect=UnavailableError("embedder down"))

    result = await classifier.classify(RouteContext(step="generate"), "anything")

    assert result == Classification(route="generate", confidence=0.0, degraded=True)
    broken = _embedding()
    broken.embed_batch = AsyncMock(side_effect=RuntimeError("down"))
    with pytest.raises(AppError) as boot:
        _ = await _classifier(broken)
    assert boot.value.code is ErrorCode.UNAVAILABLE
    with pytest.raises(AppError) as missing:
        _ = route_classifier_from_config(
            {"kind": "embedding"},
            route_names=frozenset(),
            default_route="",
            routes=_ROUTES,
        )
    assert missing.value.code is ErrorCode.INVALID_INPUT
