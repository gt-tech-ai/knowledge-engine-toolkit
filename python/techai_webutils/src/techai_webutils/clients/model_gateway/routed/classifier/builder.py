"""Config-selected ``RouteClassifier`` factory (``stub`` or ``embedding``)."""

from __future__ import annotations

from enum import StrEnum
from typing import TYPE_CHECKING, Any

from techai_webutils.clients.model_gateway.routed.classifier.stub import (
    StubRouteClassifier,
)
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.routing import RouteClassifier
    from techai_webutils.core.types.routing import Route


class RouteClassifierKind(StrEnum):
    """The route classifier backends."""

    STUB = "stub"
    """Each step's own route (the default; reproduces per-step model choice)."""
    EMBEDDING = "embedding"
    """The route whose exemplars the query is most similar to."""


def route_classifier_from_config(
    raw: Mapping[str, Any],
    *,
    route_names: frozenset[str],
    default_route: str,
    routes: Mapping[str, Route] | None = None,
    embedding: EmbeddingProvider | None = None,
) -> RouteClassifier:
    """Build the classifier ``raw["kind"]`` selects (``stub`` by default).

    Args:
        raw: The classifier config (``kind``; ``top_k`` for ``embedding``).
        route_names: The configured routes' names.
        default_route: The route a step with no route of its own uses.
        routes: The parsed routes (``embedding``: their exemplars and thresholds).
        embedding: The embedder (``embedding``; pass the per-request memo).

    Returns:
        The classifier ``raw["kind"]`` selects (``stub`` by default).

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``embedding`` without routes or an embedder.

    """
    kind = str(raw.get("kind", RouteClassifierKind.STUB))
    stub = StubRouteClassifier(route_names, default_route)
    if kind == RouteClassifierKind.STUB:
        return stub
    if kind == RouteClassifierKind.EMBEDDING:
        if routes is None or embedding is None:
            msg = "embedding route classifier requires routes and an embedder"
            raise AppError(ErrorCode.INVALID_INPUT, msg)
        from techai_webutils.clients.model_gateway.routed.classifier.embedding import (  # noqa: PLC0415
            EmbeddingRouteClassifier,
        )

        return EmbeddingRouteClassifier(
            routes, embedding, stub, top_k=int(raw.get("top_k", 3))
        )
    msg = f"unknown route classifier kind: {kind!r}"
    raise AppValueError(msg)
