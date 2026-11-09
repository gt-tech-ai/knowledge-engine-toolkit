"""OpenAI-compatible embedding provider — any server speaking ``POST /v1/embeddings``.

vLLM, Text Embeddings Inference, LiteLLM, llama.cpp's server and OpenAI itself accept
``{"model", "input": [texts]}`` and return ``{"data": [{"index", "embedding"}], "usage":
{"prompt_tokens"}}``, so one provider covers a self-hosted embedder off Bedrock. Takes an
injected ``httpx.AsyncClient`` (base_url = the server, the timeout and any bearer key
already set) so unit tests inject a mock transport; the factory owns the client's
lifetime.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, cast, override

import httpx

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.embedding import (
    EmbeddingProvider,
    EmbeddingResult,
    InputType,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource
from techai_webutils.foundation.resilience.http_boundary import httpx_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import Sequence

EMBEDDINGS_PATH = "/v1/embeddings"
"""The OpenAI embeddings endpoint, relative to the server's base URL."""


class OpenAICompatibleEmbeddingProvider(NoOpAsyncResource, EmbeddingProvider):
    """``EmbeddingProvider`` over an OpenAI-compatible ``/v1/embeddings`` server.

    (native batching).

    The response's ``usage.prompt_tokens`` covers the whole batch; it is reported on the
    first result (the others carry 0), so summing ``token_count`` over a batch gives the
    server's count.
    """

    def __init__(self, client: httpx.AsyncClient, *, model: str, dimension: int) -> None:
        """Bind the httpx client (base_url = the server).

        The model name and its vector size.
        """
        self._client = client
        self._model = model
        self._dimension = dimension

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Embed a single text (a one-element batch).

        Returns:
            The embedding of the text.

        """
        return (await self.embed_batch([text], input_type=input_type))[0]

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Embed ``texts`` in one ``/v1/embeddings`` call, returned in input order.

        ``input_type`` is applied by the factory's ``PrefixedEmbedding`` decorator, not
        here.

        Returns:
            One embedding per text, in input order.

        Raises:
            httpx_error_to_app_error: Openai-compatible embed.
            AppError: ``TIMEOUT`` on a timeout; ``UNAVAILABLE`` when the server is
                unreachable or answers 429 / 5xx (transient); ``INTERNAL`` on another
                status, a malformed body, a missing item or a vector of the wrong size.

        """
        if not texts:
            return []
        try:
            response = await self._client.post(
                EMBEDDINGS_PATH, json={"model": self._model, "input": texts}
            )
            response.raise_for_status()
            body = cast("dict[str, Any]", response.json())
        except (httpx.HTTPError, ValueError) as exc:
            raise httpx_error_to_app_error(exc, "openai-compatible embed") from exc
        vectors = self._vectors(cast("Sequence[dict[str, Any]]", body.get("data") or []))
        if len(vectors) != len(texts):
            msg = (
                f"embedding server returned {len(vectors)} vectors for {len(texts)} texts"
            )
            raise AppError(ErrorCode.INTERNAL, msg)
        usage = cast("dict[str, Any]", body.get("usage") or {})
        tokens = int(usage.get("prompt_tokens", 0))
        return [
            EmbeddingResult(
                embedding=vector, model=self._model, token_count=tokens if i == 0 else 0
            )
            for i, vector in enumerate(vectors)
        ]

    def _vectors(self, data: Sequence[dict[str, Any]]) -> list[list[float]]:
        """Return the response's vectors ordered by ``index``.

        Each checked against the dimension.

        Returns:
            The response's vectors ordered by ``index``.

        Raises:
            AppError: Embedding model ``self._model`` returned a ``len(vector)``-d vector,
                expected ``self._dimension`` (check the embedding dimension config).

        """
        ordered = sorted(data, key=lambda item: int(item.get("index", 0)))
        vectors: list[list[float]] = []
        for item in ordered:
            vector = [float(x) for x in cast("list[float]", item.get("embedding") or [])]
            if len(vector) != self._dimension:
                msg = (
                    f"embedding model {self._model!r} returned a {len(vector)}-d vector, "
                    f"expected {self._dimension} (check the embedding dimension config)"
                )
                raise AppError(ErrorCode.INTERNAL, msg)
            vectors.append(vector)
        return vectors

    @override
    def dimension(self) -> int:
        """Return the configured vector size.

        Returns:
            The configured vector size.

        """
        return self._dimension

    @override
    def model_name(self) -> str:
        """Return the served model's name.

        Returns:
            The served model's name.

        """
        return self._model
