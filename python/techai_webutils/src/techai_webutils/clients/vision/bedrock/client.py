"""Bedrock vision client: one page image, one bedrock-runtime ``Converse`` call.

``Converse`` is model-agnostic, so any Bedrock model that accepts image blocks serves;
the model is chosen per call by ``model_id``. The client takes an injected
``ConverseInvoker`` (the consumer-side ``Converse`` seam), so unit tests mock it; the
factory passes a ``LazyBedrockConverse`` that opens the aiobotocore client on first use.
"""

from __future__ import annotations

from typing import Protocol, Self, cast, final, override

from botocore.exceptions import BotoCoreError, ClientError

from techai_webutils.clients.vision.config import DEFAULT_INSTRUCTION
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.vision import VisionClient
from techai_webutils.foundation.resilience.aws_boundary import botocore_error_to_app_error


class ConverseInvoker(Protocol):
    """The bedrock-runtime ``Converse`` slice the vision client calls.

    aiobotocore is unstubbed.
    """

    async def converse(self, **kwargs: object) -> dict[str, object]:
        """Send one conversation to a model and return its reply.

        Args:
            **kwargs: The ``Converse`` request fields (``modelId``, ``messages``,
                ``inferenceConfig``).

        Returns:
            The ``Converse`` response.

        """
        ...

    async def aclose(self) -> None:
        """Release the underlying client, if one was opened."""
        ...


@final
class BedrockVisionClient(VisionClient):
    """``VisionClient`` that asks a Bedrock multimodal model to transcribe each page.

    The client owns the invoker: ``aclose`` (and the async context exit) releases it.
    """

    def __init__(
        self,
        client: ConverseInvoker,
        *,
        max_tokens: int,
        instruction: str = DEFAULT_INSTRUCTION,
    ) -> None:
        """Bind the invoker, the reply's token ceiling and the instruction.

        Args:
            client: Calls bedrock-runtime ``Converse``.
            max_tokens: The reply's token ceiling per page.
            instruction: The text sent beside each page image.

        """
        self._client = client
        self._max_tokens = max_tokens
        self._instruction = instruction

    @override
    async def describe_page(self, png: bytes, *, model_id: str) -> str:
        """Transcribe one page with a single ``Converse`` call at temperature 0.

        Args:
            png: The rendered page, PNG-encoded.
            model_id: The Bedrock model to ask.

        Returns:
            The reply's text blocks joined, stripped; empty when the page has no text.

        Raises:
            AppError: A reply stopped at ``max_tokens`` or with no message
                (``INTERNAL``).
            botocore_error_to_app_error: A botocore failure, coded (throttling / 5xx /
                connection → ``UNAVAILABLE``, otherwise ``INTERNAL``).

        """
        try:
            response = await self._client.converse(
                modelId=model_id,
                messages=[
                    {
                        "role": "user",
                        "content": [
                            {"image": {"format": "png", "source": {"bytes": png}}},
                            {"text": self._instruction},
                        ],
                    }
                ],
                inferenceConfig={"maxTokens": self._max_tokens, "temperature": 0.0},
            )
        except (ClientError, BotoCoreError) as exc:
            raise botocore_error_to_app_error(exc, "bedrock vision") from exc
        if response.get("stopReason") == "max_tokens":
            msg = (
                f"Bedrock model {model_id!r} stopped at max_tokens "
                f"({self._max_tokens}) before the page ended"
            )
            raise AppError(ErrorCode.INTERNAL, msg)
        return _text(response, model_id)

    async def aclose(self) -> None:
        """Release the invoker's client."""
        await self._client.aclose()

    async def __aenter__(self) -> Self:
        """Enter the async context; the client opens lazily on the first page.

        Returns:
            This instance.

        """
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Release the client on context exit.

        Args:
            *exc: The exception triple, if any.

        """
        await self.aclose()


def _text(response: dict[str, object], model_id: str) -> str:
    """Join the text blocks of a ``Converse`` reply.

    Args:
        response: The ``Converse`` response.
        model_id: The model asked, for the error message.

    Returns:
        The text blocks joined, stripped.

    Raises:
        AppError: The response carries no ``output.message.content`` (``INTERNAL``).

    """
    output = cast("dict[str, object]", response.get("output") or {})
    message = cast("dict[str, object]", output.get("message") or {})
    content = message.get("content")
    if not isinstance(content, list):
        msg = f"Bedrock model {model_id!r} returned no message content"
        raise AppError(ErrorCode.INTERNAL, msg)
    blocks = cast("list[dict[str, object]]", content)
    return "".join(str(b["text"]) for b in blocks if "text" in b).strip()
