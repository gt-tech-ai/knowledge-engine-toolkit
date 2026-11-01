"""Route-decision sinks: where each finalized ``RouteDecision`` goes."""

from techai_webutils.clients.model_gateway.routed.sink.builder import (
    RouteDecisionSinkKind,
    route_decision_sink_from_config,
)

__all__ = ["RouteDecisionSinkKind", "route_decision_sink_from_config"]
