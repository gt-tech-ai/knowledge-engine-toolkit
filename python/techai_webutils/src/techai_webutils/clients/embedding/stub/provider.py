"""Deterministic stub embedding provider for no-infra builds/tests.

``StubEmbeddingProvider`` derives a deterministic vector from the input text (a SHA-256
counter expansion), so a retrieval-adjacent service can build and unit-test the embedding
path with no Ollama. Same text ⇒ same vector; different text ⇒ different vector; vectors
are the configured dimension. Mirrors the ``ollama`` sibling's shape
(``NoOpAsyncResource`` + ``EmbeddingProvider``).
"""

from __future__ import annotations

import hashlib
from typing import final, override

from techai_webutils.core.interfaces.embedding import (
    EmbeddingProvider,
    EmbeddingResult,
    InputType,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource


def _deterministic_vector(text: str, dimension: int) -> list[float]:
    """Expand ``text`` into a deterministic ``dimension``-length vector.

    Values are in [-1, 1], derived via SHA-256.

    Args:
        text: The text.
        dimension: The vector size.

    Returns:
        The deterministic vector.

    """
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


@final
class StubEmbeddingProvider(NoOpAsyncResource, EmbeddingProvider):
    """An ``EmbeddingProvider`` returning deterministic vectors (dev/test).

    It makes no external calls.
    """

    def __init__(self, model: str, dimension: int) -> None:
        """Bind the reported model name and the vector dimension the stub produces.

        Args:
            model: The model name to report.
            dimension: The vector size.

        """
        self._model = model
        self._dimension = dimension

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Return a deterministic embedding for a single text.

        ``input_type`` does not change it.

        Args:
            text: The text to embed.
            input_type: Whether ``text`` is a stored document or a search query;
                asymmetric models embed the two differently.

        Returns:
            A deterministic embedding for a single text.

        """
        return EmbeddingResult(
            embedding=_deterministic_vector(text, self._dimension),
            model=self._model,
            token_count=0,
        )

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Return deterministic embeddings for multiple texts, preserving order.

        Args:
            texts: The texts to embed.
            input_type: Whether the texts are stored documents or search queries.

        Returns:
            Deterministic embeddings for multiple texts, preserving order.

        """
        return [await self.embed(text, input_type=input_type) for text in texts]

    @override
    def dimension(self) -> int:
        """Return the configured embedding dimension.

        Returns:
            The configured embedding dimension.

        """
        return self._dimension

    @override
    def model_name(self) -> str:
        """Return the stub model identifier.

        Returns:
            The stub model identifier.

        """
        return self._model
