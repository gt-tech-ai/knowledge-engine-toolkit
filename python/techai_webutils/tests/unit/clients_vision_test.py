"""Unit tests for the vision clients: the factory, the Bedrock backend, its decoration."""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock, patch

import pytest
from botocore.exceptions import ClientError

from techai_webutils.clients.vision import VisionConfig, VisionKind, vision_from_config
from techai_webutils.clients.vision.bedrock import (
    BedrockVisionClient,
    ConverseInvoker,
    LazyBedrockConverse,
)
from techai_webutils.clients.vision.decorated import decorate
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.vision import VisionClient
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations

if TYPE_CHECKING:
    from aiobotocore.config import AioConfig

_MODEL = "vision-model"
_PNG = b"\x89PNG-page"
_MAX_TOKENS = 512
_TIMEOUT = 45.0
_REGION = "eu-west-1"


def _runtime(response: dict[str, object] | Exception) -> AsyncMock:
    """Build a mock Converse invoker answering with ``response``, or raising it.

    Args:
        response: The Converse response, or the error the call raises.

    Returns:
        The mock.

    """
    runtime = AsyncMock(spec=ConverseInvoker)
    converse = cast("AsyncMock", runtime.converse)
    if isinstance(response, Exception):
        converse.side_effect = response
    else:
        converse.return_value = response
    return runtime


def _answer(*texts: str, stop: str = "end_turn") -> dict[str, object]:
    """Build a Converse response whose message holds ``texts`` as text blocks.

    Args:
        *texts: The text blocks.
        stop: The response's ``stopReason``.

    Returns:
        The response.

    """
    return {
        "output": {"message": {"content": [{"text": t} for t in texts]}},
        "stopReason": stop,
    }


@pytest.mark.asyncio
async def test_vision_factory_builds_stub_by_default_and_refuses_unknown_kind() -> None:
    """Test that the factory boots on the stub and fails loudly on a kind it lacks.

    **Why this test is important:**
      - A composition root builds the vision client at start-up; the default must need no
        AWS account, and a mistyped kind must stop the boot rather than silently read
        nothing.

    **What it tests:**
      - ``VisionConfig()`` builds a ``VisionClient`` that reads every page as empty text.
      - ``bedrock`` builds the Bedrock backend with no I/O.
      - An unknown kind, or ``bedrock`` with ``max_tokens`` below 1, raises
        ``AppValueError`` coded ``INVALID_INPUT``.
    """
    stub = vision_from_config(VisionConfig())
    assert isinstance(stub, VisionClient)
    assert not await stub.describe_page(_PNG, model_id=_MODEL)

    bedrock = vision_from_config(VisionConfig(kind=VisionKind.BEDROCK))
    assert isinstance(bedrock, BedrockVisionClient)

    with pytest.raises(AppValueError, match="unknown vision kind") as raised:
        _ = vision_from_config(
            VisionConfig(kind=cast("VisionKind", cast("object", "paper")))
        )
    assert raised.value.code is ErrorCode.INVALID_INPUT
    with pytest.raises(AppValueError, match="max_tokens"):
        _ = vision_from_config(VisionConfig(kind=VisionKind.BEDROCK, max_tokens=0))


@pytest.mark.asyncio
async def test_bedrock_vision_sends_the_page_image_and_returns_its_text() -> None:
    """Test that one page is one Converse call carrying the PNG, answered with its text.

    **Why this test is important:**
      - A scanned page's only text is what the model reads off the image; a request
        without the image, or a reply whose blocks are dropped, indexes a blank page.

    **What it tests:**
      - ``describe_page`` calls Converse once with ``model_id``, a user turn holding the
        PNG bytes as a ``png`` image block and an instruction, ``maxTokens`` from the
        client and temperature 0.
      - The reply's text blocks come back joined, stripped.
    """
    runtime = _runtime(_answer("# Title\n", "body text  "))
    client = BedrockVisionClient(runtime, max_tokens=_MAX_TOKENS)

    text = await client.describe_page(_PNG, model_id=_MODEL)

    assert text == "# Title\nbody text"
    converse = cast("AsyncMock", runtime.converse)
    converse.assert_awaited_once()
    request = cast(
        "dict[str, object]", cast("list[object]", converse.await_args_list)[0].kwargs
    )
    assert request["modelId"] == _MODEL
    assert request["inferenceConfig"] == {"maxTokens": _MAX_TOKENS, "temperature": 0.0}
    [turn] = cast("list[dict[str, object]]", request["messages"])
    assert turn["role"] == "user"
    image, instruction = cast("list[dict[str, object]]", turn["content"])
    assert image == {"image": {"format": "png", "source": {"bytes": _PNG}}}
    assert isinstance(instruction["text"], str)
    assert instruction["text"]


@pytest.mark.asyncio
async def test_bedrock_vision_codes_failures_and_refuses_a_truncated_page() -> None:
    """Test that SDK failures and a cut-off transcription surface as coded errors.

    **Why this test is important:**
      - The client tier retries only transient codes, and page OCR turns any coded error
        into a failed document naming the page; a raw botocore error would escape both,
        and a reply cut at ``maxTokens`` would index half a page as if it were whole.

    **What it tests:**
      - A throttled call raises an ``AppError`` coded ``UNAVAILABLE``.
      - An access-denied call raises one coded ``INTERNAL``.
      - A reply stopped at ``max_tokens`` raises one coded ``INTERNAL`` naming the limit.
      - A reply with no message content raises one coded ``INTERNAL``.
    """
    for aws_code, expected in (
        ("ThrottlingException", ErrorCode.UNAVAILABLE),
        ("AccessDeniedException", ErrorCode.INTERNAL),
    ):
        error = ClientError({"Error": {"Code": aws_code, "Message": "no"}}, "Converse")
        client = BedrockVisionClient(_runtime(error), max_tokens=_MAX_TOKENS)
        with pytest.raises(AppError) as raised:
            _ = await client.describe_page(_PNG, model_id=_MODEL)
        assert raised.value.code is expected

    truncated = BedrockVisionClient(
        _runtime(_answer("half a pa", stop="max_tokens")), max_tokens=_MAX_TOKENS
    )
    with pytest.raises(AppError, match="max_tokens") as raised:
        _ = await truncated.describe_page(_PNG, model_id=_MODEL)
    assert raised.value.code is ErrorCode.INTERNAL

    empty = BedrockVisionClient(_runtime({"stopReason": "end_turn"}), max_tokens=1)
    with pytest.raises(AppError, match="no message content") as raised:
        _ = await empty.describe_page(_PNG, model_id=_MODEL)
    assert raised.value.code is ErrorCode.INTERNAL


@pytest.mark.asyncio
async def test_decorated_vision_forwards_describe_page() -> None:
    """Test that the decorated client runs ``describe_page`` through the client tier.

    **Why this test is important:**
      - The composition root wraps the vision client in timeout and breaker; a wrapper
        that dropped the image or the model id would read another page or none.

    **What it tests:**
      - ``decorate(...).describe_page(png, model_id=...)`` awaits the wrapped client once
        with the same arguments and returns its text.
    """
    inner = AsyncMock(spec=VisionClient)
    cast("MagicMock", inner.describe_page).return_value = "page text"
    client = decorate(
        inner, Decorations(decoration.default_config(), Collaborators()), "vision"
    )

    assert await client.describe_page(_PNG, model_id=_MODEL) == "page text"
    cast("AsyncMock", inner.describe_page).assert_awaited_once_with(_PNG, model_id=_MODEL)


@pytest.mark.asyncio
async def test_lazy_converse_opens_one_timed_client_and_closes_it() -> None:
    """Test that the runtime opens one bedrock-runtime client, timed, on the first page.

    **Why this test is important:**
      - Construction must do no I/O so a composition root can build it at boot; a client
        opened per page would leak connections; and a timeout built but never handed to
        the SDK leaves a hung model call holding a parse slot forever.

    **What it tests:**
      - Building ``LazyBedrockConverse`` opens nothing.
      - Two pages through ``BedrockVisionClient`` open ``bedrock-runtime`` once, in the
        configured region, with connect and read timeouts both ``timeout_seconds``.
      - Leaving the client's context closes the SDK client once; a second close is a
        no-op.
    """
    sdk = AsyncMock(spec=ConverseInvoker)
    cast("AsyncMock", sdk.converse).return_value = _answer("text")
    opened = MagicMock()
    opened.__aenter__ = AsyncMock(return_value=sdk)
    opened.__aexit__ = AsyncMock(return_value=None)
    session = MagicMock()
    create_client = cast("MagicMock", session.create_client)
    create_client.return_value = opened

    with patch(
        "techai_webutils.clients.vision.bedrock.runtime.aiobotocore.session.get_session",
        return_value=session,
    ):
        runtime = LazyBedrockConverse(region=_REGION, timeout_seconds=_TIMEOUT)
    create_client.assert_not_called()

    async with BedrockVisionClient(runtime, max_tokens=_MAX_TOKENS) as client:
        for _ in range(2):
            assert await client.describe_page(_PNG, model_id=_MODEL) == "text"

    create_client.assert_called_once()
    assert create_client.call_args.args == ("bedrock-runtime",)
    assert create_client.call_args.kwargs["region_name"] == _REGION
    sdk_config = cast("AioConfig", create_client.call_args.kwargs["config"])
    assert (sdk_config.connect_timeout, sdk_config.read_timeout) == (_TIMEOUT, _TIMEOUT)
    cast("AsyncMock", opened.__aexit__).assert_awaited_once()
    await runtime.aclose()
    cast("AsyncMock", opened.__aexit__).assert_awaited_once()
