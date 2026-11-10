"""The ``noop`` route-decision sink: decisions go nowhere (the default)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.routing import RouteDecisionSink

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.model_gateway import RouteContext, RouteDecision


class NoopRouteDecisionSink(RouteDecisionSink):
    """Drops every decision."""

    @override
    def record(self, decision: RouteDecision, ctx: RouteContext) -> None:
        """Drop ``decision``.

        Args:
            decision: How the gateway routed the call.
            ctx: The request's routing context.

        """
        del decision, ctx
