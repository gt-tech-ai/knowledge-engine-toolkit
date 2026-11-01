"""Unit tests for the ``RoutingPlan`` core type."""

from __future__ import annotations

from techai_webutils.core.types.routing import (
    ContextStrategy,
    PlanRequirements,
    ReasoningLevel,
    RoutingPlan,
)


def test_routing_plan_defaults_are_complete() -> None:
    """Test that every ``RoutingPlan`` field has a default.

    **Why this test is important:**
      - Later stories fill the plan (escalation, stickiness, learned priors); a required field
        would make each of them a breaking reshape of the contract.

    **What it tests:**
      - ``RoutingPlan()`` builds with medium reasoning, the full context and no fallbacks
      - a plan built from just a model id keeps every other default
    """
    plan = RoutingPlan()
    chosen = RoutingPlan(model_id="anthropic/sonnet")

    assert (plan.reasoning_level, plan.context_strategy) == (
        ReasoningLevel.MEDIUM,
        ContextStrategy.FULL,
    )
    assert (plan.fallback_model_ids, plan.requirements, plan.escalate) == (
        (),
        PlanRequirements(),
        False,
    )
    assert chosen == RoutingPlan(
        model_id="anthropic/sonnet", confidence=0.0, max_cost_usd=None
    )
