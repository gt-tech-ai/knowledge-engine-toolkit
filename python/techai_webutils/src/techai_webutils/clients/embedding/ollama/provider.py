"""Ollama embedding provider — local dev embeddings via the Ollama ``/api/embed`` endpoint.

Uses the modern batching endpoint: ``POST /api/embed`` with ``{"model", "input": [texts]}`` returning
``{"embeddings": [[...], …]}`` (plural key). Takes an injected ``httpx.AsyncClient`` (base_url = the
Ollama host) so unit tests inject a mock; the factory that builds it owns the client's lifetime (it is
process-lived — see ``new_embedding_from_config``).
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    import httpx


class OllamaEmbeddingProvider(NoOpAsyncResource, EmbeddingProvider):
    """``EmbeddingProvider`` backed by a local Ollama server (``/api/embed``, native batching).

    ``token_count`` is always 0 — Ollama's embedding response carries no token count.
    """

    def __init__(self, client: httpx.AsyncClient, model: str, dimension: int) -> None:
        """Bind the httpx client (base_url = Ollama host), the model name, and its embedding dimension."""
        self._client = client
        self._model = model
        self._dimension = dimension

    async def embed(self, text: str) -> EmbeddingResult:
        """Embed a single text (a one-element batch)."""
        return (await self.embed_batch([text]))[0]

    async def embed_batch(self, texts: list[str]) -> list[EmbeddingResult]:
        """Embed multiple texts in ONE ``/api/embed`` call (Ollama batches the ``input`` list).

        Raises ``ValueError`` on an unexpected response shape (missing ``embeddings`` key) or a
        dimension mismatch (a vector whose length differs from the configured ``dimension``), so a
        model/config mismatch fails here with a clear message rather than as a silent no-op far
        downstream at vector-store upsert or search time.
        """
        response = await self._client.post(
            "/api/embed", json={"model": self._model, "input": texts}
        )
        response.raise_for_status()
        body = response.json()
        if "embeddings" not in body:
            msg = f"Ollama /api/embed response missing 'embeddings' key: {body!r}"
            raise AppValueError(msg)
        results: list[EmbeddingResult] = []
        for vector in body["embeddings"]:
            if len(vector) != self._dimension:
                msg = (
                    f"Ollama model {self._model!r} returned a {len(vector)}-d vector, "
                    f"expected {self._dimension} (check embedding.dimension config)"
                )
                raise AppValueError(msg)
            results.append(
                EmbeddingResult(embedding=list(vector), model=self._model, token_count=0)
            )
        return results

    def dimension(self) -> int:
        """Return the model's embedding dimension (configured; 768 for nomic-embed-text)."""
        return self._dimension

    def model_name(self) -> str:
        """Return the Ollama model identifier."""
        return self._model
