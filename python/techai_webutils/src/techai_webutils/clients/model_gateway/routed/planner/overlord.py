"""The ``overlord`` route planner: ask a remote planner, fail open to ``local``.

Each plan sends a ``PlanRequest`` (the verified tenant and workspace, the team and user
the call is charged to, step, query, the filtered candidates, the requirements and the
policy) through the injected ``RouteTransport`` with the caller's service token, under
``timeout_ms``, one retry on ``UNAVAILABLE`` and a circuit breaker. The returned
``RoutingPlan``'s model and fallbacks are mapped onto the offered endpoints in order.

The remote planner is never a dependency of the answer: a timeout, a transport error, an
    open
breaker, or a plan naming a model the request didn't offer (``routing_plan_invalid``,
defense in depth for the filter's data policy) makes the injected ``local`` planner answer
instead, with ``decider=local`` and ``reason=planner_fail_open``, counted as
``routed_planner_fail_open_total{cause}``.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass, replace
from typing import TYPE_CHECKING, Any

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.types.routing import Plan, PlanRequest
from techai_webutils.foundation.logger.logger import get_logger
from techai_webutils.foundation.resilience.async_circuit_breaker import (
    AsyncCircuitBreaker,
)
from techai_webutils.foundation.resilience.async_retry import retry_transient_async
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.interfaces.routing import RoutePlanner, RouteTransport
    from techai_webutils.core.types.routing import (
        CatalogEndpointRef,
        PlanInput,
        RoutingPlan,
    )

_RETRY_DELAY_S = 0.02
"""Backoff before the single retry on ``UNAVAILABLE`` (the planner's budget is
milliseconds)."""


@dataclass(frozen=True, slots=True)
class OverlordPlannerConfig:
    """Where the remote planner is and how long it may take."""

    target: str
    """The planner's address (``host:port``)."""
    timeout_ms: int = 300
    """The per-attempt deadline in milliseconds."""
    service_token: str = ""
    """The caller's service token (from the secrets layer), sent as a bearer token."""
    breaker_failures: int = 5
    """Consecutive failures that open the breaker."""
    breaker_recovery_s: float = 30.0
    """Seconds the open breaker waits before a trial call."""

    @classmethod
    def from_mapping(cls, raw: Mapping[str, Any]) -> OverlordPlannerConfig:
        """Return the config in ``raw`` (the planner's ``overlord`` block).

        Returns:
            The config in ``raw`` (the planner's ``overlord`` block).

        Raises:
            AppError: ``INVALID_INPUT`` without a ``target``.

        """
        target = str(raw.get("target", ""))
        if not target:
            raise AppError(ErrorCode.INVALID_INPUT, "overlord planner requires a target")
        return cls(
            target=target,
            timeout_ms=int(raw.get("timeout_ms", 300)),
            service_token=str(raw.get("service_token", "")),
            breaker_failures=int(raw.get("breaker_failures", 5)),
            breaker_recovery_s=float(raw.get("breaker_recovery_s", 30.0)),
        )


class _InvalidPlanError(Exception):
    """A remote plan named a model the request didn't offer (never leaves this module)."""


class OverlordPlanner:
    """Plans remotely through ``transport``.

    The ``fallback`` planner answers when it can't.
    """

    def __init__(
        self,
        transport: RouteTransport,
        fallback: RoutePlanner,
        config: OverlordPlannerConfig,
        *,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Bind the transport, the local fallback, the config and the metrics."""
        self._transport = transport
        self._fallback = fallback
        self._config = config
        self._timeout_s = config.timeout_ms / 1000
        self._metadata = (
            (("authorization", f"Bearer {config.service_token}"),)
            if config.service_token
            else ()
        )
        self._breaker = AsyncCircuitBreaker(
            config.breaker_failures, config.breaker_recovery_s
        )
        self._call_retried = retry_transient_async(
            max_attempts=2, base_delay=_RETRY_DELAY_S
        )(self._call)
        self._logger = get_logger("overlord_planner")
        self._fail_open: MetricCounter | None = None
        if metrics is not None:
            self._fail_open = metrics.counter(
                "routed_planner_fail_open_total",
                "Plans the local planner answered because the overlord couldn't.",
                ["cause"],
            )

    async def _call(self, request: PlanRequest) -> RoutingPlan:
        """Call the transport once under the deadline (``TimeoutError`` past it).

        Returns:
            The plan from the transport.

        """
        return await asyncio.wait_for(
            self._transport.route(
                request, timeout_s=self._timeout_s, metadata=self._metadata
            ),
            self._timeout_s,
        )

    async def _remote(self, request: PlanRequest) -> RoutingPlan:
        """Return the remote plan behind the breaker, retrying ``UNAVAILABLE`` once.

        Returns:
            The remote plan behind the breaker, retrying ``UNAVAILABLE`` once.

        Raises:
            AppError: Circuit breaker suppressed a call's outcome.

        """
        async with self._breaker:
            return await self._call_retried(request)
        # The breaker never suppresses an exception, so this is unreachable.
        raise AppError(ErrorCode.INTERNAL, "circuit breaker suppressed a call's outcome")

    @staticmethod
    def _ordered(
        plan: RoutingPlan, candidates: tuple[CatalogEndpointRef, ...]
    ) -> tuple[CatalogEndpointRef, ...]:
        """Map the plan's model and fallbacks onto the offered endpoints, in order.

        Returns:
            The endpoints in plan order.

        Raises:
            _InvalidPlanError: the plan names no model, or one the request didn't offer.

        """
        models = (plan.model_id, *plan.fallback_model_ids)
        offered = {ref.model_id for ref in candidates}
        if not plan.model_id or any(model not in offered for model in models):
            raise _InvalidPlanError
        seen: dict[str, None] = dict.fromkeys(models)
        return tuple(ref for model in seen for ref in candidates if ref.model_id == model)

    async def plan(self, ctx: RouteContext, route_input: PlanInput) -> Plan:
        """Return the remote plan.

        Or the local one when the remote planner can't answer.

        Returns:
            The remote plan.

        """
        request = PlanRequest(
            org_id=ctx.tenant_id,
            workspace_id=ctx.workspace_id,
            step=ctx.step,
            query=route_input.query,
            candidates=route_input.candidates,
            requirements=route_input.requirements,
            policy=route_input.policy,
            team_id=ctx.team_id,
            user_id=ctx.user_id,
        )
        try:
            remote = await self._remote(request)
            ordered = self._ordered(remote, route_input.candidates)
        except _InvalidPlanError:
            return await self._failed_open(ctx, route_input, "routing_plan_invalid")
        except CircuitOpenError:
            return await self._failed_open(ctx, route_input, "breaker_open")
        except TimeoutError:
            return await self._failed_open(ctx, route_input, "timeout")
        except Exception as exc:
            cause = (
                str(exc.code).lower() if isinstance(exc, AppError) else "transport_error"
            )
            return await self._failed_open(ctx, route_input, cause)
        return Plan(
            route=remote.task_class,
            confidence=remote.confidence,
            ordered=ordered,
            decider="overlord",
            reason="escalated" if remote.escalate else "planned",
        )

    async def _failed_open(
        self, ctx: RouteContext, route_input: PlanInput, cause: str
    ) -> Plan:
        """Log and count the failure.

        Then return the local plan marked ``planner_fail_open``.

        Returns:
            The local plan, marked as failed open.

        """
        self._logger.warning("overlord planner failed open", cause=cause, step=ctx.step)
        if self._fail_open is not None:
            self._fail_open.inc(1.0, cause=cause)
        local = await self._fallback.plan(ctx, route_input)
        return replace(local, decider="local", reason="planner_fail_open")
