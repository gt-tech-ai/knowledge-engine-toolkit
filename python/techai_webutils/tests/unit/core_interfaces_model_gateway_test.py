"""Unit tests for the ``ModelGateway`` contract and ``RouteContext``.

They also cover the route-decision slot.
"""

from __future__ import annotations

import asyncio
import dataclasses

import pytest

from techai_webutils.clients.llm.stub import StubLlmProvider
from techai_webutils.clients.model_gateway.passthrough import PassthroughGateway
from techai_webutils.core.interfaces.llm import LLMProvider
from techai_webutils.core.interfaces.model_gateway import (
    ModelGateway,
    RouteContext,
    RouteDecision,
    current_route_decision,
    set_route_decision,
)


def test_model_gateway_satisfies_llm_provider() -> None:
    """Test that a gateway is a drop-in ``LLMProvider``.

    **Why this test is important:**
      - The composition root swaps a provider for a gateway with no pipeline change; that
        only holds if every gateway is an ``LLMProvider``.

    **What it tests:**
      - ``ModelGateway`` subclasses ``LLMProvider``; a passthrough gateway is an instance
        of both
    """
    gateway = PassthroughGateway(StubLlmProvider())

    assert issubclass(ModelGateway, LLMProvider)
    assert isinstance(gateway, LLMProvider)
    assert isinstance(gateway, ModelGateway)


def test_route_context_and_decision_defaults() -> None:
    """Test that the routing value types default every field and are immutable.

    **Why this test is important:**
      - Later routing stages fill more fields; every field defaulted means they extend the
        decision without reshaping it, and immutability means the audit reads what routing
        set.

    **What it tests:**
      - ``RouteContext()`` leaves ``slo`` unset (empty) with no ``model_preference``
      - ``RouteDecision()`` defaults to ``mode="user"``, ``decider="local"``, no attempts,
        no costs
      - assigning a ``RouteDecision`` field raises ``FrozenInstanceError``
    """
    ctx, decision = RouteContext(), RouteDecision()

    assert (ctx.slo, ctx.model_preference, ctx.tenant_id) == ("", None, "")
    assert (decision.mode, decision.decider, decision.attempts) == ("user", "local", ())
    assert (decision.estimated_cost, decision.actual_cost, decision.shadow) == (
        None,
        None,
        False,
    )
    with pytest.raises(dataclasses.FrozenInstanceError):
        decision.reason = "x"


@pytest.mark.asyncio
async def test_route_decision_slot_is_request_scoped() -> None:
    """Test that a decision set in one task is not seen by another.

    **Why this test is important:**
      - Concurrent requests share a process; one request's routing decision must never be
        audited on another's record.

    **What it tests:**
      - with nothing set, ``current_route_decision()`` is ``None``
      - a decision set inside a child task is visible there and not in the parent
    """

    async def child() -> RouteDecision | None:
        """Set a route decision inside a child task.

        Returns:
            The child's decision.

        """
        await asyncio.sleep(0)
        set_route_decision(RouteDecision(reason="child"))
        return current_route_decision()

    seen = await asyncio.create_task(child())

    assert seen == RouteDecision(reason="child")
    assert current_route_decision() is None
