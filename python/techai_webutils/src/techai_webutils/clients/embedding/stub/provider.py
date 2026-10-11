"""Deterministic stub embedding provider for no-infra builds/tests.

``StubEmbeddingProvider`` derives a deterministic vector from the input text (a SHA-256 counter
expansion), so a retrieval-adjacent service can build and unit-test the embedding path with no
Ollama. Same text ⇒ same vector; different text ⇒ different vector; vectors are the configured
dimension. Mirrors the ``ollama`` sibling's shape (``NoOpAsyncResource`` + ``EmbeddingProvider``).
"""

from __future__ import annotations

import hashlib

from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


def _deterministic_vector(text: str, dimension: int) -> list[float]:
    """Expand ``text`` into a deterministic ``dimension``-length vector in [-1, 1] via SHA-256."""
    values: list[float] = []
    counter = 0
    while len(values) < dimension:
        block = hashlib.sha256(f"{text}:{counter}".encode()).digest()
        for byte in block:
            values.append(byte / 255.0 * 2 - 1)
            if len(values) >= dimension:
                break
        counter += 1
    return values


class StubEmbeddingProvider(NoOpAsyncResource, EmbeddingProvider):
    """An ``EmbeddingProvider`` returning deterministic vectors with no external calls (dev/test)."""

    def __init__(self, model: str, dimension: int) -> None:
        """Bind the reported model name and the vector dimension the stub produces."""
        self._model = model
        self._dimension = dimension

    async def embed(self, text: str) -> EmbeddingResult:
        """Return a deterministic embedding for a single text."""
        return EmbeddingResult(
            embedding=_deterministic_vector(text, self._dimension),
            model=self._model,
            token_count=0,
        )

    async def embed_batch(self, texts: list[str]) -> list[EmbeddingResult]:
        """Return deterministic embeddings for multiple texts, preserving order."""
        return [await self.embed(text) for text in texts]

    def dimension(self) -> int:
        """Return the configured embedding dimension."""
        return self._dimension

    def model_name(self) -> str:
        """Return the stub model identifier."""
        return self._model
