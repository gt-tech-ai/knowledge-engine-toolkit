"""Config-selected ``RoutePlanner`` factory (``local`` or ``overlord``)."""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.model_gateway.routed.planner.local import LocalPlanner
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.clients.model_gateway.routed.planner.overlord import (
        OverlordPlanner as _LazyOverlordPlanner,
    )
    from techai_webutils.clients.model_gateway.routed.planner.overlord import (
        OverlordPlannerConfig as _LazyOverlordPlannerConfig,
    )
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


@dataclass(frozen=True, kw_only=True)
class RoutePlannerDependencies:
    """What ``route_planner_from_config`` takes, grouped into one value."""

    classifier: RouteClassifier
    """The local classifier."""

    ranker: CandidateRanker
    """The local ranker."""

    routes: Mapping[str, Route]
    """The parsed routes."""

    planner_deps: Mapping[str, object] | None = None
    """Collaborators a remote kind needs (``overlord``: ``transport``, the product's
    ``RouteTransport``).
    """

    metrics: MetricsProvider | None = None
    """Counts the overlord's fail-opens."""


def route_planner_from_config(
    raw: Mapping[str, object], options: RoutePlannerDependencies
) -> RoutePlanner:
    """Build the planner ``raw["kind"]`` selects (``local`` by default).

    Args:
        raw: The planner config (``kind``; ``overlord: {target, timeout_ms,
            service_token}``).
        options: The settings and collaborators, grouped.

    Returns:
        The planner ``raw["kind"]`` selects (``local`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``overlord`` without a target or a transport.

    """
    kind = str(raw.get("kind", RoutePlannerKind.LOCAL))
    local = LocalPlanner(options.classifier, options.ranker, options.routes)
    if kind == RoutePlannerKind.LOCAL:
        return local
    if kind == RoutePlannerKind.OVERLORD:
        overlord_planner_cls = cast(
            "type[_LazyOverlordPlanner]",
            importlib.import_module(
                "techai_webutils.clients.model_gateway.routed.planner.overlord"
            ).OverlordPlanner,
        )
        overlord_planner_config_cls = cast(
            "type[_LazyOverlordPlannerConfig]",
            importlib.import_module(
                "techai_webutils.clients.model_gateway.routed.planner.overlord"
            ).OverlordPlannerConfig,
        )

        config = overlord_planner_config_cls.from_mapping(
            cast("Mapping[str, object]", raw.get("overlord", {}))
        )
        transport = (options.planner_deps or {}).get("transport")
        if transport is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "overlord planner requires a transport"
            )
        return overlord_planner_cls(
            cast("RouteTransport", transport), local, config, metrics=options.metrics
        )
    msg = f"unknown route planner kind: {kind!r}"
    raise AppValueError(msg)
