"""``ShadowRouter`` — plan a request under an alternative configuration, never serve it.

Each live call submits its filtered ``PlanInput``; the shadow planner (the
``routed.shadow`` classifier / ranker / planner) plans it in a background task and hands a
``shadow=True`` decision to the recorder. No model is called and nothing else is written.
At most ``max_pending`` shadow plans run at once; a submission over that bound is dropped,
so a shadow can never queue behind or delay the live answer. A shadow failure is swallowed
(it is an experiment, not a dependency). ``drain()`` awaits the in-flight plans at
shutdown.
"""

from __future__ import annotations

import asyncio
import contextlib
from typing import TYPE_CHECKING, final

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from techai_webutils.core.interfaces.model_gateway import RouteContext, RouteDecision
    from techai_webutils.core.interfaces.routing import RoutePlanner
    from techai_webutils.core.types.routing import Plan, PlanInput

    type ShadowRecorder = Callable[[RouteDecision, RouteContext], None]
    """Receives each shadow decision (the gateway's guarded sink call)."""
    type DecisionBuilder = Callable[[Plan, Mapping[str, str]], RouteDecision | None]
    """Turns a shadow plan plus the filter's rejections into its decision."""


@final
class ShadowRouter:
    """Runs the shadow plan for each live call in bounded background tasks."""

    def __init__(
        self,
        planner: RoutePlanner,
        build: DecisionBuilder,
        record: ShadowRecorder,
        *,
        max_pending: int = 16,
    ) -> None:
        """Bind the shadow planner, the decision builder, the recorder and the bound.

        Args:
            planner: The shadow planner.
            build: Builds the shadow decision from a plan.
            record: Records the shadow decision.
            max_pending: The most shadow plans in flight; more are dropped.

        """
        self._planner = planner
        self._build = build
        self._record = record
        self._max_pending = max_pending
        self._pending: set[asyncio.Task[None]] = set()

    def submit(
        self, ctx: RouteContext, route_input: PlanInput, rejections: Mapping[str, str]
    ) -> None:
        """Start a shadow plan for this call, or drop it when ``max_pending`` are running.

        Args:
            ctx: The call's route context.
            route_input: The plan's input: the query, its requirements and the step.
            rejections: Why each filtered-out endpoint was dropped.

        """
        if len(self._pending) >= self._max_pending:
            return
        task = asyncio.create_task(self._run(ctx, route_input, rejections))
        self._pending.add(task)
        task.add_done_callback(self._pending.discard)

    async def _run(
        self, ctx: RouteContext, route_input: PlanInput, rejections: Mapping[str, str]
    ) -> None:
        """Plan, build the shadow decision and record it; swallow any failure.

        Args:
            ctx: The call's route context.
            route_input: The plan's input: the query, its requirements and the step.
            rejections: Why each filtered-out endpoint was dropped.

        """
        with contextlib.suppress(Exception):
            plan = await self._planner.plan(ctx, route_input)
            decision = self._build(plan, rejections)
            if decision is not None:
                self._record(decision, ctx)

    async def drain(self) -> None:
        """Await every in-flight shadow plan."""
        if self._pending:
            _ = await asyncio.gather(*self._pending, return_exceptions=True)
