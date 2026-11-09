"""Config-selected ``RouteDecisionSink`` factory (``noop`` or ``analytics``)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.sink.noop import NoopRouteDecisionSink
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.routing import RouteDecisionSink


class RouteDecisionSinkKind(StrEnum):
    """The route-decision sink backends."""

    NOOP = "noop"
    """Decisions go nowhere (the default)."""
    ANALYTICS = "analytics"
    """One ``route_decisions`` fact per decision, through the fact publisher."""


def route_decision_sink_from_config(
    raw: Mapping[str, Any], *, facts: FactPublisher | None = None
) -> RouteDecisionSink:
    """Build the sink ``raw["kind"]`` selects (``noop`` by default).

    Returns:
        The sink ``raw["kind"]`` selects (``noop`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``analytics`` without a fact publisher.

    """
    kind = str(raw.get("kind", RouteDecisionSinkKind.NOOP))
    if kind == RouteDecisionSinkKind.NOOP:
        return NoopRouteDecisionSink()
    if kind == RouteDecisionSinkKind.ANALYTICS:
        if facts is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "analytics sink requires a fact publisher"
            )
        from techai_webutils.clients.model_gateway.routed.sink.analytics import (  # noqa: PLC0415
            AnalyticsRouteDecisionSink,
        )

        return AnalyticsRouteDecisionSink(facts)
    msg = f"unknown route decision sink kind: {kind!r}"
    raise AppValueError(msg)
