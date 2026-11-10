"""Bedrock embedding provider: Amazon Titan text embeddings (v2).

Calls go to bedrock-runtime ``InvokeModel``.

Titan v2 takes one text per call (``{"inputText", "dimensions", "normalize"}``) and
returns ``{"embedding": [...], "inputTextTokenCount": n}``, so ``embed_batch`` fans out
one call per text. The provider takes an injected ``BedrockRuntimeInvoker`` (the
consumer-side ``InvokeModel`` seam), so unit tests mock it; the factory passes a
``LazyBedrockRuntime`` that opens the aiobotocore client on first use.
"""

from __future__ import annotations

import asyncio
import json
from typing import TYPE_CHECKING, Protocol, Self, TypedDict, Unpack, cast, final, override

from botocore.exceptions import BotoCoreError, ClientError

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.embedding import (
    EmbeddingProvider,
    EmbeddingResult,
    InputType,
)
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error

if TYPE_CHECKING:
    from collections.abc import Awaitable


class _Readable(Protocol):
    """An InvokeModel response body.

    aiobotocore's async ``StreamingBody`` or a sync byte stream.
    """

    def read(self) -> bytes | Awaitable[bytes]:
        """Read the whole body.

        Returns:
            The body's bytes, or an awaitable of them.

        """
        ...


class InvokeModelRequest(TypedDict):
    """The keyword arguments of the AWS ``invoke_model`` call."""

    modelId: str
    """The AWS ``modelId`` request field."""

    body: str
    """The AWS ``body`` request field."""

    contentType: str
    """The AWS ``contentType`` request field."""

    accept: str
    """The AWS ``accept`` request field."""


class BedrockRuntimeInvoker(Protocol):
    """The bedrock-runtime ``InvokeModel`` slice the embedding provider calls.

    aiobotocore is unstubbed.
    """

    async def invoke_model(
        self, **kwargs: Unpack[InvokeModelRequest]
    ) -> dict[str, object]:
        """Invoke a model with a JSON body; the response's ``body`` is a readable stream.

        Args:
            **kwargs: The ``InvokeModel`` request fields.

        Returns:
            The ``InvokeModel`` response.

        """
        ...

    async def aclose(self) -> None:
        """Release the underlying client, if one was opened."""
        ...


@final
class BedrockEmbeddingProvider(EmbeddingProvider):
    """``EmbeddingProvider`` backed by an Amazon Titan text-embedding model on Bedrock.

    ``token_count`` is Titan's ``inputTextTokenCount``. The provider owns the invoker:
    ``aclose`` (and the async context exit) releases it.
    """

    def __init__(
        self,
        client: BedrockRuntimeInvoker,
        *,
        model: str,
        dimension: int,
        normalize: bool = True,
    ) -> None:
        """Bind the invoker, the Titan model id, the vector size and normalize flag.

        Args:
            client: Calls bedrock-runtime ``InvokeModel``.
            model: The Titan model id.
            dimension: The vector size to request.
            normalize: Whether to ask for unit-length vectors.

        """
        self._client = client
        self._model = model
        self._dimension = dimension
        self._normalize = normalize

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Embed one text with a single ``InvokeModel`` call.

        Titan is symmetric: ``input_type`` does not change the request.

        Raises a coded ``AppError``: a botocore failure maps through
        ``botocore_error_to_app_error`` (throttling / 5xx / connection → ``UNAVAILABLE``,
        otherwise ``INTERNAL``), and a vector whose length differs from the configured
        dimension is ``INTERNAL``.

        Args:
            text: The text to embed.
            input_type: Whether ``text`` is a stored document or a search query;
                asymmetric models embed the two differently.

        Returns:
            The embedding of the text.

        Raises:
            AppError: Bedrock model ``self._model`` returned a ``len(vector)``-d vector,
                expected ``self._dimension`` (check the embedding dimension config).
            botocore_error_to_app_error: Bedrock embed.

        """
        body = json.dumps({
            "inputText": text,
            "dimensions": self._dimension,
            "normalize": self._normalize,
        })
        try:
            response = await self._client.invoke_model(
                modelId=self._model,
                body=body,
                contentType="application/json",
                accept="application/json",
            )
            payload = await _decode(response)
        except (ClientError, BotoCoreError) as exc:
            raise botocore_error_to_app_error(exc, "bedrock embed") from exc
        vector = cast("list[float]", payload.get("embedding") or [])
        if len(vector) != self._dimension:
            msg = (
                f"Bedrock model {self._model!r} returned a {len(vector)}-d vector, "
                f"expected {self._dimension} (check the embedding dimension config)"
            )
            raise AppError(ErrorCode.INTERNAL, msg)
        return EmbeddingResult(
            embedding=[float(x) for x in vector],
            model=self._model,
            token_count=int(cast("int", payload.get("inputTextTokenCount", 0))),
        )

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Embed each text with its own call, concurrently, in input order.

        Titan v2 has no batch body.

        Args:
            texts: The texts to embed.
            input_type: Whether the texts are stored documents or search queries.

        Returns:
            One embedding per text, in input order.

        """
        return list(
            await asyncio.gather(*(self.embed(t, input_type=input_type) for t in texts))
        )

    @override
    def dimension(self) -> int:
        """Return the configured vector size (Titan v2 supports 256, 512 or 1024).

        Returns:
            The configured vector size (Titan v2 supports 256, 512 or 1024).

        """
        return self._dimension

    @override
    def model_name(self) -> str:
        """Return the Bedrock model id.

        Returns:
            The Bedrock model id.

        """
        return self._model

    async def aclose(self) -> None:
        """Release the invoker's client."""
        await self._client.aclose()

    @override
    async def __aenter__(self) -> Self:
        """Enter the async context; the client opens lazily on first call.

        Returns:
            This instance.

        """
        return self

    @override
    async def __aexit__(self, *exc: object) -> None:
        """Release the client on context exit.

        Args:
            *exc: The exception triple, if any.

        """
        await self.aclose()


async def _decode(response: dict[str, object]) -> dict[str, object]:
    """Read and decode an ``InvokeModel`` response's JSON body.

    Args:
        response: The ``InvokeModel`` response.

    Returns:
        The decoded body.

    """
    body = await _read(cast("_Readable", response["body"]))
    return cast("dict[str, object]", json.loads(body))


async def _read(stream: _Readable) -> bytes:
    """Read an InvokeModel response body.

    aiobotocore's async ``StreamingBody`` or a sync stream.

    Args:
        stream: The response body.

    Returns:
        An InvokeModel response body.

    """
    data = stream.read()
    if isinstance(data, bytes):
        return data
    return await data
