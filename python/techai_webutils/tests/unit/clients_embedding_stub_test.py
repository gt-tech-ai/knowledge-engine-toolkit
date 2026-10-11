"""Tests for the deterministic stub embedding backend + factory selection.

The ``stub`` embedder lets a retrieval-adjacent service build and unit-test the embedding path with
no Ollama — the stub-first property (ARCHITECTURE.md#stub-first-backends).
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.embedding.builder import (
    EmbeddingConfig,
    EmbeddingKind,
    new_embedding_from_config,
)
from techai_webutils.clients.embedding.stub import StubEmbeddingProvider


@pytest.mark.asyncio
async def test_embedding_stub_returns_deterministic_vector() -> None:
    """The stub returns a deterministic, correctly-sized vector — same text ⇒ same vector.

    Why this test is important:
        - A stub embedder is only useful for a no-infra build if it is DETERMINISTIC (a test can
          assert on it) and produces vectors of the configured dimension (so a downstream vector
          store accepts them); a random or wrong-length vector would break both.

    What it tests:
        - Two embeds of the same text return the same vector of length ``dimension``; a different
          text yields a different vector; ``dimension``/``model_name`` report the config.
    """
    stub = StubEmbeddingProvider(model="stub", dimension=8)

    first = await stub.embed("hello")
    second = await stub.embed("hello")
    other = await stub.embed("world")

    assert len(first.embedding) == 8
    assert first.embedding == second.embedding, "same text embeds to the same vector"
    assert first.embedding != other.embedding, (
        "different text embeds to a different vector"
    )
    assert stub.dimension() == 8
    assert stub.model_name() == "stub"

    batch = await stub.embed_batch(["hello", "world"])
    assert [r.embedding for r in batch] == [first.embedding, other.embedding]


def test_new_embedding_from_config_selects_stub() -> None:
    """The factory returns the stub embedder for ``EmbeddingKind.STUB`` (config-selects-impl).

    Why this test is important:
        - The stub is opted into by a config kind; if the factory silently built the Ollama client
          (an ``httpx`` connection), the configured no-infra behavior would never take effect.

    What it tests:
        - ``new_embedding_from_config`` with ``kind=STUB`` returns a ``StubEmbeddingProvider`` of the
          configured dimension.
    """
    provider = new_embedding_from_config(
        EmbeddingConfig(kind=EmbeddingKind.STUB, dimension=16)
    )
    assert isinstance(provider, StubEmbeddingProvider)
    assert provider.dimension() == 16
