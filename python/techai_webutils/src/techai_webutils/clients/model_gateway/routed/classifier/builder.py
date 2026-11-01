"""Config-selected ``RouteClassifier`` factory (``stub`` today; ``embedding`` follows)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.classifier.stub import (
    StubRouteClassifier,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.routing import RouteClassifier


class RouteClassifierKind(StrEnum):
    """The route classifier backends."""

    STUB = "stub"
    """Each step's own route (the default; reproduces per-step model choice)."""


def route_classifier_from_config(
    raw: Mapping[str, Any], *, route_names: frozenset[str], default_route: str
) -> RouteClassifier:
    """Build the classifier ``raw["kind"]`` selects (``stub`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    kind = str(raw.get("kind", RouteClassifierKind.STUB))
    if kind == RouteClassifierKind.STUB:
        return StubRouteClassifier(route_names, default_route)
    msg = f"unknown route classifier kind: {kind!r}"
    raise AppValueError(msg)
