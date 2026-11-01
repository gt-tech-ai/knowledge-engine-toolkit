"""The ``analytics`` route-decision sink: one ``route_decisions`` fact per decision.

Dims carry who and what (route, model, endpoint, decider, mode, reason, shadow, workspace, step);
measures carry how much (attempts, tokens, confidence and the estimated and actual cost when
known). Publishing goes through the injected ``FactPublisher``, which never blocks.
"""

from __future__ import annotations

import uuid
from datetime import UTC, datetime
from typing import TYPE_CHECKING

from techai_webutils.core.types.fact import Fact

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.model_gateway import RouteContext, RouteDecision
    from techai_webutils.core.interfaces.routing import RouteDecisionSink

CUBE = "route_decisions"
"""The analytics cube route decisions land in."""


class AnalyticsRouteDecisionSink:
    """Publishes each decision as a ``route_decisions`` fact."""

    def __init__(self, facts: FactPublisher) -> None:
        """Bind the fact publisher."""
        self._facts = facts

    def record(self, decision: RouteDecision, ctx: RouteContext) -> None:
        """Publish ``decision`` as one fact under ``ctx``'s tenant."""
        measures: dict[str, float] = {
            "attempts": float(len(decision.attempts)),
            "tokens": float(decision.tokens),
            "confidence": decision.confidence,
        }
        if decision.estimated_cost is not None:
            measures["estimated_cost"] = float(decision.estimated_cost)
        if decision.actual_cost is not None:
            measures["actual_cost"] = float(decision.actual_cost)
        fact = Fact(
            cube=CUBE,
            org_id=ctx.tenant_id,
            ts=datetime.now(UTC),
            dims={
                "route": decision.route,
                "model_id": decision.model_id,
                "endpoint": decision.endpoint,
                "decider": decision.decider,
                "mode": decision.mode,
                "reason": decision.reason,
                "shadow": "true" if decision.shadow else "false",
                "workspace_id": ctx.workspace_id,
                "step": ctx.step,
            },
            measures=measures,
            idempotency_key=str(uuid.uuid4()),
        )
        self._facts.publish([fact])


if TYPE_CHECKING:
    _INTERFACE_CHECK: type[RouteDecisionSink] = AnalyticsRouteDecisionSink
