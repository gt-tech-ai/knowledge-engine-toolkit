"""The ``stub`` route classifier: each step's own route, else the default route."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.types.routing import Classification

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.model_gateway import RouteContext


class StubRouteClassifier:
    """Routes a step to the route named for it (confidence 1.0).

    Else to ``default_route``.
    """

    def __init__(self, route_names: frozenset[str], default_route: str) -> None:
        """Bind the known route names and the fallback route (empty: none)."""
        self._route_names = route_names
        self._default_route = default_route

    async def classify(self, ctx: RouteContext, query: str) -> Classification:
        """Return the step's route, else the default route.

        Returns:
            The step's route, else the default route.

        Raises:
            AppError: ``INVALID_INPUT`` when the step has no route and no default is set.

        """
        del query
        if ctx.step in self._route_names:
            return Classification(route=ctx.step, confidence=1.0)
        if self._default_route:
            return Classification(route=self._default_route, confidence=1.0)
        msg = f"no route for step {ctx.step!r} and no default route"
        raise AppError(ErrorCode.INVALID_INPUT, msg)
