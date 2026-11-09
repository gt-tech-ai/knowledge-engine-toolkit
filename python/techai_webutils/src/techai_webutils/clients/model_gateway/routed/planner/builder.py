"""Config-selected ``RoutePlanner`` factory (``local`` or ``overlord``)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any, cast

from techai_webutils.clients.model_gateway.routed.planner.local import LocalPlanner
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.routing import (
        CandidateRanker,
        RouteClassifier,
        RoutePlanner,
        RouteTransport,
    )
    from techai_webutils.core.types.routing import Route


class RoutePlannerKind(StrEnum):
    """The route planner backends."""

    LOCAL = "local"
    """Classify and rank in process (the default)."""
    OVERLORD = "overlord"
    """Ask the remote overlord planner; fail open to ``local``."""


def route_planner_from_config(
    raw: Mapping[str, Any],
    *,
    classifier: RouteClassifier,
    ranker: CandidateRanker,
    routes: Mapping[str, Route],
    planner_deps: Mapping[str, object] | None = None,
    metrics: MetricsProvider | None = None,
) -> RoutePlanner:
    """Build the planner ``raw["kind"]`` selects (``local`` by default).

    Args:
        raw: The planner config (``kind``; ``overlord: {target, timeout_ms,
            service_token}``).
        classifier: The local classifier.
        ranker: The local ranker.
        routes: The parsed routes.
        planner_deps: Collaborators a remote kind needs (``overlord``: ``transport``, the
            product's ``RouteTransport``).
        metrics: Counts the overlord's fail-opens.

    Returns:
        The planner ``raw["kind"]`` selects (``local`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``overlord`` without a target or a transport.

    """
    kind = str(raw.get("kind", RoutePlannerKind.LOCAL))
    local = LocalPlanner(classifier, ranker, routes)
    if kind == RoutePlannerKind.LOCAL:
        return local
    if kind == RoutePlannerKind.OVERLORD:
        from techai_webutils.clients.model_gateway.routed.planner.overlord import (  # noqa: PLC0415
            OverlordPlanner,
            OverlordPlannerConfig,
        )

        config = OverlordPlannerConfig.from_mapping(raw.get("overlord", {}))
        transport = (planner_deps or {}).get("transport")
        if transport is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "overlord planner requires a transport"
            )
        return OverlordPlanner(
            cast("RouteTransport", transport), local, config, metrics=metrics
        )
    msg = f"unknown route planner kind: {kind!r}"
    raise AppValueError(msg)
