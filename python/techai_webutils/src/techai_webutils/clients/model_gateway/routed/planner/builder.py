"""Config-selected ``RoutePlanner`` factory (``local`` today; ``overlord`` follows)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.planner.local import LocalPlanner
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.routing import (
        CandidateRanker,
        RouteClassifier,
        RoutePlanner,
    )
    from techai_webutils.core.types.routing import Route


class RoutePlannerKind(StrEnum):
    """The route planner backends."""

    LOCAL = "local"
    """Classify and rank in process (the default)."""


def route_planner_from_config(
    raw: Mapping[str, Any],
    *,
    classifier: RouteClassifier,
    ranker: CandidateRanker,
    routes: Mapping[str, Route],
) -> RoutePlanner:
    """Build the planner ``raw["kind"]`` selects (``local`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    kind = str(raw.get("kind", RoutePlannerKind.LOCAL))
    if kind == RoutePlannerKind.LOCAL:
        return LocalPlanner(classifier, ranker, routes)
    msg = f"unknown route planner kind: {kind!r}"
    raise AppValueError(msg)
