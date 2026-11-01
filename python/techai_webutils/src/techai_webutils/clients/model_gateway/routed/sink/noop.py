"""The ``noop`` route-decision sink: decisions go nowhere (the default)."""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.model_gateway import RouteContext, RouteDecision
    from techai_webutils.core.interfaces.routing import RouteDecisionSink


class NoopRouteDecisionSink:
    """Drops every decision."""

    def record(self, decision: RouteDecision, ctx: RouteContext) -> None:
        """Drop ``decision``."""


if TYPE_CHECKING:
    _INTERFACE_CHECK: type[RouteDecisionSink] = NoopRouteDecisionSink
