"""A routing policy store over a periodically refreshed snapshot of stored policies.

``RoutingPolicyStore.policy`` is synchronous and runs on every routed call, so policies
kept in a database cannot be read per request. This store holds an in-memory snapshot an
injected async loader fills, refreshes it on an interval, and answers from it without I/O:

- the snapshot's ``(tenant, workspace)`` entry, else its tenant-wide ``(tenant, "")``
  entry, else the fallback store (the policies configuration gives), so a tenant with
  nothing stored keeps routing exactly as before;
- a refresh replaces the snapshot as a whole, and a failed one keeps the last good
  snapshot, logs the failure and counts it.

It is a ``ManagedResource``: entering performs one refresh (a failure does not stop
startup) and starts the refresh loop; exiting cancels the loop.
"""

from __future__ import annotations

import asyncio
import contextlib
from types import MappingProxyType
from typing import TYPE_CHECKING, Self, final

from techai_webutils.clients.model_gateway.routed.catalog import invalid
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Mapping
    from types import TracebackType

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.routing import RoutingPolicyStore
    from techai_webutils.core.types.routing import RoutingPolicy

type PolicyKey = tuple[str, str]
"""(tenant id, workspace id); an empty workspace id is the tenant-wide policy."""

type PolicyLoader = Callable[[], Awaitable[Mapping[PolicyKey, RoutingPolicy]]]
"""Returns every stored policy, keyed by ``PolicyKey``."""

_TENANT_WIDE = ""
"""The workspace id of a tenant-wide policy."""


@final
class RefreshingPolicyStore:
    """Answers policy lookups from a snapshot an async loader refreshes on an interval."""

    def __init__(
        self,
        loader: PolicyLoader,
        *,
        fallback: RoutingPolicyStore,
        interval_s: float,
        metrics: MetricsProvider | None = None,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        """Build the store; nothing is loaded until ``refresh`` or entering the context.

        Args:
            loader: Returns every stored policy (the caller's client carries its timeout).
            fallback: Answers a lookup the snapshot has no entry for.
            interval_s: Seconds between refreshes; must be positive.
            metrics: Counts ``routing_policy_refresh_total{outcome}`` when given.
            sleep: Waits between refreshes (injected so tests drive the loop).

        A callee raises ``AppError``: ``INVALID_INPUT`` when ``interval_s`` is not
        positive.

        Raises:
            invalid: Routing policy refresh interval must be positive, got ``interval_s``.

        """
        if interval_s <= 0:
            msg = f"routing policy refresh interval must be positive, got {interval_s}"
            raise invalid(msg)
        self._loader = loader
        self._fallback = fallback
        self._interval_s = interval_s
        self._sleep = sleep
        self._snapshot: Mapping[PolicyKey, RoutingPolicy] = MappingProxyType({})
        self._task: asyncio.Task[None] | None = None
        self._logger = get_logger("refreshing_policy_store")
        self._refreshes: MetricCounter | None = None
        if metrics is not None:
            self._refreshes = metrics.counter(
                "routing_policy_refresh_total",
                "Routing policy snapshot refreshes, by outcome (ok, failed).",
                ["outcome"],
            )

    def policy(self, tenant_id: str, workspace_id: str) -> RoutingPolicy:
        """Return the stored workspace policy, else the stored tenant one.

        Otherwise the fallback's.

        Args:
            tenant_id: The tenant.
            workspace_id: The workspace within the tenant.

        Returns:
            The stored workspace policy, else the stored tenant one.

        """
        snapshot = self._snapshot
        stored = snapshot.get((tenant_id, workspace_id))
        if stored is None:
            stored = snapshot.get((tenant_id, _TENANT_WIDE))
        if stored is not None:
            return stored
        return self._fallback.policy(tenant_id, workspace_id)

    async def refresh(self) -> None:
        """Load every stored policy and swap the snapshot; keep the old one on failure."""
        try:
            loaded = await self._loader()
        except Exception as exc:
            self._logger.exception("routing policy refresh failed", error=str(exc))
            self._count("failed")
            return
        self._snapshot = MappingProxyType(dict(loaded))
        self._count("ok")

    async def __aenter__(self) -> Self:
        """Refresh once, then start the refresh loop.

        Returns:
            This instance.

        """
        await self.refresh()
        self._task = asyncio.create_task(self._run())
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Cancel the refresh loop and wait for it to stop.

        Args:
            exc_type: The exception's type, if any.
            exc_val: The exception, if any.
            exc_tb: Its traceback, if any.

        """
        task, self._task = self._task, None
        if task is None:
            return
        _ = task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await task

    async def _run(self) -> None:
        """Refresh every ``interval_s`` until cancelled."""
        while True:
            await self._sleep(self._interval_s)
            await self.refresh()

    def _count(self, outcome: str) -> None:
        """Count one refresh with its outcome, when metrics are wired.

        Args:
            outcome: ``ok`` or ``failed``.

        """
        if self._refreshes is not None:
            self._refreshes.inc(outcome=outcome)
