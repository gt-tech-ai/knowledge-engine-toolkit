"""``vision_from_config``: the config-selected ``VisionClient``."""

from __future__ import annotations

import importlib
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.vision.config import VisionConfig, VisionKind
from techai_webutils.clients.vision.stub.client import StubVisionClient
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.vision.bedrock import (
        BedrockVisionClient as _LazyBedrockVisionClient,
    )
    from techai_webutils.clients.vision.bedrock import (
        LazyBedrockConverse as _LazyLazyBedrockConverse,
    )
    from techai_webutils.core.interfaces.vision import VisionClient


def vision_from_config(config: VisionConfig) -> VisionClient:
    """Build the ``VisionClient`` ``config.kind`` selects, with no I/O.

    Args:
        config: Selects the backend and carries its settings.

    Returns:
        The ``VisionClient`` ``config.kind`` selects; ``bedrock`` opens its AWS client on
        the first page.

    Raises:
        AppValueError: An unknown kind, or ``max_tokens`` below 1 (``INVALID_INPUT``).

    """
    if config.kind == VisionKind.STUB:
        return StubVisionClient()
    if config.kind == VisionKind.BEDROCK:
        if config.max_tokens < 1:
            msg = f"vision: max_tokens must be at least 1, got {config.max_tokens}"
            raise AppValueError(msg)
        bedrock = importlib.import_module("techai_webutils.clients.vision.bedrock")
        client_cls = cast("type[_LazyBedrockVisionClient]", bedrock.BedrockVisionClient)
        runtime_cls = cast("type[_LazyLazyBedrockConverse]", bedrock.LazyBedrockConverse)
        return client_cls(
            runtime_cls(region=config.region, timeout_seconds=config.timeout_seconds),
            max_tokens=config.max_tokens,
            instruction=config.instruction,
        )
    msg = f"unknown vision kind: {config.kind!r}"
    raise AppValueError(msg)
