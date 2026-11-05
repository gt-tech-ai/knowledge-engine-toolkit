"""Unit tests for the config-selected ``ModelGateway`` factory and the ``passthrough`` kind."""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.llm.builder import LlmConfig, new_llm_from_config
from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.model_gateway import (
    GatewayConfig,
    GatewayKind,
    new_model_gateway_from_config,
)
from techai_webutils.clients.model_gateway.passthrough import PassthroughGateway
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.llm import LLMMessage, StreamUsage
from techai_webutils.core.interfaces.model_gateway import (
    RouteContext,
    RouteDecision,
    current_route_decision,
)

_MESSAGES = [LLMMessage(role="user", content="route me please")]


@pytest.mark.asyncio
async def test_passthrough_gateway_is_indistinguishable_from_wrapped_provider() -> None:
    """Test that the passthrough gateway answers exactly as the provider it wraps.

    **Why this test is important:**
      - This is the migration guarantee: switching the composition root onto the gateway
        must change nothing a caller can observe.

    **What it tests:**
      - ``complete`` returns an equal response (content, model, tokens)
      - ``stream`` and ``stream_with_usage`` yield the same items, usage included
      - ``model_name`` matches and ``wrapped_provider`` is the inner provider
    """
    inner = StubLlmProvider()
    gateway = PassthroughGateway(inner)

    assert await gateway.complete(_MESSAGES) == await inner.complete(_MESSAGES)
    assert [c async for c in await gateway.stream(_MESSAGES)] == [
        c async for c in await inner.stream(_MESSAGES)
    ]
    with_usage = [c async for c in await gateway.stream_with_usage(_MESSAGES)]
    assert with_usage == [c async for c in await inner.stream_with_usage(_MESSAGES)]
    assert isinstance(with_usage[-1], StreamUsage)
    assert gateway.model_name() == inner.model_name()
    assert gateway.wrapped_provider is inner
    async with gateway as entered:
        assert entered is gateway


@pytest.mark.asyncio
async def test_passthrough_resolve_ignores_context_returns_inner() -> None:
    """Test that the passthrough gateway resolves every context to its one provider.

    **Why this test is important:**
      - Passthrough is the no-routing baseline; a context-dependent answer would be routing.

    **What it tests:**
      - two different ``RouteContext``s both resolve to the wrapped provider
    """
    inner = StubLlmProvider()
    gateway = PassthroughGateway(inner)

    assert await gateway.resolve(RouteContext()) is inner
    assert (
        await gateway.resolve(
            RouteContext(tenant_id="org-1", step="generate", slo="cost")
        )
        is inner
    )


def test_new_model_gateway_from_config_passthrough_fronts_llm_factory() -> None:
    """Test that the default kind wraps the provider the LLM factory builds from ``config.llm``.

    **Why this test is important:**
      - The gateway must inherit today's per-step model choice, which lives in ``LlmConfig``.

    **What it tests:**
      - the default ``GatewayConfig`` builds a ``PassthroughGateway``
      - the injected ``provider_factory`` is called once with ``config.llm`` and its result wrapped
    """
    llm = LlmConfig(model="stub-model")
    provider = StubLlmProvider("stub-model")
    factory = MagicMock(return_value=provider)

    gateway = new_model_gateway_from_config(
        GatewayConfig(llm=llm), provider_factory=factory
    )

    assert isinstance(gateway, PassthroughGateway)
    factory.assert_called_once_with(llm)
    assert gateway.wrapped_provider is provider
    assert GatewayConfig().kind == GatewayKind.PASSTHROUGH


def test_new_model_gateway_from_config_unknown_kind_raises() -> None:
    """Test that a misconfigured gateway fails at build time.

    **Why this test is important:**
      - A typo in the gateway kind must stop boot, never silently fall back to one model.

    **What it tests:**
      - an unknown kind raises a ``ValueError`` coded ``INVALID_INPUT``
    """
    with pytest.raises(ValueError, match="unknown model gateway kind") as caught:
        new_model_gateway_from_config(
            GatewayConfig(kind="magic"), provider_factory=new_llm_from_config
        )

    assert isinstance(caught.value, AppError)
    assert caught.value.code is ErrorCode.INVALID_INPUT


@pytest.mark.asyncio
async def test_passthrough_sets_route_decision_for_complete_and_stream() -> None:
    """Test that every served call leaves a ``passthrough`` route decision.

    **Why this test is important:**
      - The audit and tracing decorators outside the gateway read which model served a call
        from the decision; a stream can't carry it on its chunks.

    **What it tests:**
      - after ``complete``, the decision is ``reason="passthrough"``, ``decider="local"`` with
        the wrapped model as ``model_id`` and ``endpoint``
      - after a fully consumed ``stream``, the same decision is set (it is unset until then)
    """
    gateway = PassthroughGateway(StubLlmProvider("stub-model"))
    expected = RouteDecision(
        reason="passthrough",
        decider="local",
        model_id="stub-model",
        endpoint="stub-model",
    )

    async def consume() -> tuple[RouteDecision | None, RouteDecision | None]:
        stream = await gateway.stream(_MESSAGES)
        before = current_route_decision()
        _ = [chunk async for chunk in stream]
        return before, current_route_decision()

    # A fresh task copies this (still empty) context, so the stream's decision stays in it.
    before, after_stream = await asyncio.create_task(consume())
    await gateway.complete(_MESSAGES)
    after_complete = current_route_decision()

    assert after_complete == expected
    assert before is None
    assert after_stream == expected
