"""Unit tests for the semantic cache verifier kinds and their factory."""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.semantic_cache.verifier import (
    CacheVerifierConfig,
    CacheVerifierKind,
    StubCacheVerifier,
    cache_verifier_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMMessage, LLMProvider, LLMResponse


def _llm(reply: str | None = None, error: Exception | None = None) -> MagicMock:
    """Return an ``LLMProvider`` mock whose ``complete`` answers ``reply``.

    Or raises ``error``.

    Args:
        reply: What the model answers.
        error: What the model raises; ``None`` answers.

    Returns:
        An ``LLMProvider`` mock whose ``complete`` answers ``reply``.

    """
    llm = MagicMock(spec=LLMProvider)
    llm.complete = AsyncMock(
        side_effect=error,
        return_value=LLMResponse(
            content=reply or "",
            model="m",
            input_tokens=1,
            output_tokens=1,
            finish_reason="stop",
        ),
    )
    return llm


@pytest.mark.asyncio
async def test_stub_verifier_never_confirms() -> None:
    """Test that the default verifier confirms nothing.

    **Why this test is important:**
      - The stub is the zero-cost default: with it, a listed ``verified`` tier serves
        nothing, so turning the tier on needs an explicit model choice.

    **What it tests:**
      - the factory's default is a ``StubCacheVerifier`` and it answers ``False``
    """
    verifier = cache_verifier_from_config(CacheVerifierConfig())

    assert isinstance(verifier, StubCacheVerifier)
    assert await verifier.same_question("a", "a") is False


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("reply", "error", "expected"),
    [
        ("Yes.", None, True),
        ("yes, both ask it", None, True),
        ("No", None, False),
        ("", None, False),
        (None, RuntimeError("down"), False),
    ],
    ids=["yes", "yes-lowercase", "no", "empty", "error"],
)
async def test_llm_verifier_serves_only_on_a_yes(
    reply: str | None, error: Exception | None, *, expected: bool
) -> None:
    """Test that the model verifier confirms only on a reply that starts with "yes".

    **Why this test is important:**
      - A borderline neighbour is served only on an explicit confirmation; any other
        reply, or a failed call, must be a miss so the band never serves a wrong answer.

    **What it tests:**
      - "Yes." and "yes, …" confirm; "No", an empty reply and a raising model do not
      - both questions reach the model in one user message

    Args:
        reply: What the model answers.
        error: What the model raises; ``None`` answers.
        expected: Whether the hit is served.

    """
    llm = _llm(reply, error)
    verifier = cache_verifier_from_config(
        CacheVerifierConfig(kind=CacheVerifierKind.LLM), llm=llm
    )

    assert await verifier.same_question("cached question", "new question") is expected
    messages = cast(
        "list[LLMMessage]",
        cast(
            "tuple[object, ...]",
            cast("MagicMock", cast("AsyncMock", llm.complete).await_args).args,
        )[0],
    )
    prompt = messages[-1].content
    assert "cached question" in prompt
    assert "new question" in prompt


@pytest.mark.parametrize(
    ("config", "llm"),
    [(CacheVerifierConfig(kind="llm"), None), (CacheVerifierConfig(kind="oracle"), None)],
    ids=["llm-without-provider", "unknown"],
)
def test_verifier_factory_fails_loudly(
    config: CacheVerifierConfig, llm: LLMProvider | None
) -> None:
    """Test that the factory refuses a verifier it cannot build.

    **Why this test is important:**
      - A missing model or a typo in the kind must stop startup, not silently confirm
        nothing.

    **What it tests:**
      - ``llm`` without a provider and an unknown kind raise ``INVALID_INPUT``

    Args:
        config: The verifier settings.
        llm: The model.

    """
    with pytest.raises(AppError) as caught:
        _ = cache_verifier_from_config(config, llm=llm)
    assert caught.value.code is ErrorCode.INVALID_INPUT
