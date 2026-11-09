"""Unit tests for the refreshing routing policy store."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.model_gateway.routed.policy_store import (
    routing_policy_store_from_config,
)
from techai_webutils.clients.model_gateway.routed.refreshing_policy_store import (
    RefreshingPolicyStore,
)
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
from techai_webutils.core.types.routing import RoutingPolicy

if TYPE_CHECKING:
    from collections.abc import Mapping

_EXPECTED_AWAIT_COUNT = 3

_FALLBACK = routing_policy_store_from_config({
    "policies": {"org-3": {"pins": {"generate": "config/model"}}},
})
"""The config store the refreshing store falls back to."""


def _pinned(model: str) -> RoutingPolicy:
    """Return a policy pinning ``generate`` to ``model``.

    Returns:
        A policy pinning ``generate`` to ``model``.

    """
    return RoutingPolicy(pins={"generate": model})


def _metrics() -> tuple[MagicMock, MagicMock]:
    """Return a mocked metrics provider and the counter it hands out.

    Returns:
        A mocked metrics provider and the counter it hands out.

    """
    counter = MagicMock(spec=MetricCounter)
    metrics = MagicMock(spec=MetricsProvider)
    metrics.counter.return_value = counter
    return metrics, counter


@pytest.mark.asyncio
async def test_lookup_prefers_workspace_then_tenant_then_fallback() -> None:
    """Test the lookup order over the refreshed snapshot and the fallback store.

    **Why this test is important:**
      - A stored workspace policy must beat its tenant's, and an org with nothing stored
        must keep the policy configuration gives it, or rollout changes routing for
        everyone.

    **What it tests:**
      - before any refresh every lookup is the fallback's
      - after a refresh: the workspace entry, then the tenant entry (empty workspace id),
        then the fallback, which also serves a tenant with nothing stored
    """
    loader = AsyncMock(
        return_value={
            ("org-1", ""): _pinned("tenant/model"),
            ("org-1", "ws-a"): _pinned("workspace/model"),
        }
    )
    store = RefreshingPolicyStore(loader, fallback=_FALLBACK, interval_s=30)

    assert store.policy("org-1", "ws-a") == RoutingPolicy()
    await store.refresh()

    assert store.policy("org-1", "ws-a").pin("generate") == "workspace/model"
    assert store.policy("org-1", "ws-b").pin("generate") == "tenant/model"
    assert store.policy("org-3", "ws-a").pin("generate") == "config/model"
    assert store.policy("org-2", "ws-a") == RoutingPolicy()


@pytest.mark.asyncio
async def test_refresh_replaces_the_snapshot_whole() -> None:
    """Test that a refresh swaps in the loader's mapping as a whole.

    **Why this test is important:**
      - A deleted policy must stop applying on the next refresh, and a reader must never
        see entries from two different loads mixed together.

    **What it tests:**
      - an entry missing from the second load no longer resolves
      - the store keeps its own copy, so mutating the loader's mapping changes nothing
    """
    first: dict[tuple[str, str], RoutingPolicy] = {("org-1", ""): _pinned("a")}
    second: Mapping[tuple[str, str], RoutingPolicy] = {("org-2", ""): _pinned("b")}
    loader = AsyncMock(side_effect=[first, second])
    store = RefreshingPolicyStore(loader, fallback=_FALLBACK, interval_s=30)

    await store.refresh()
    first["org-1", ""] = _pinned("mutated")
    assert store.policy("org-1", "").pin("generate") == "a"

    await store.refresh()
    assert store.policy("org-1", "") == RoutingPolicy()
    assert store.policy("org-2", "").pin("generate") == "b"


@pytest.mark.asyncio
async def test_failed_refresh_keeps_the_last_snapshot_and_counts() -> None:
    """Test that a loader error leaves the previous snapshot in place.

    **Why this test is important:**
      - An aiops outage must not drop every tenant back to its config policy mid-flight.

    **What it tests:**
      - ``refresh`` does not raise when the loader does
      - the earlier snapshot still answers
      - ``routing_policy_refresh_total`` counts ``ok`` then ``failed``
    """
    metrics, counter = _metrics()
    loader = AsyncMock(side_effect=[{("org-1", ""): _pinned("a")}, RuntimeError("down")])
    store = RefreshingPolicyStore(
        loader, fallback=_FALLBACK, interval_s=30, metrics=metrics
    )

    await store.refresh()
    await store.refresh()

    assert store.policy("org-1", "").pin("generate") == "a"
    assert metrics.counter.call_args.args[0] == "routing_policy_refresh_total"
    assert [c.kwargs for c in counter.inc.call_args_list] == [
        {"outcome": "ok"},
        {"outcome": "failed"},
    ]


def test_rejects_a_non_positive_interval() -> None:
    """Test that the refresh interval must be positive.

    **Why this test is important:**
      - A zero interval would spin the refresh loop and hammer the policy owner.

    **What it tests:**
      - ``interval_s=0`` raises ``INVALID_INPUT``
    """
    with pytest.raises(AppError) as caught:
        RefreshingPolicyStore(AsyncMock(), fallback=_FALLBACK, interval_s=0)
    assert caught.value.code is ErrorCode.INVALID_INPUT


class _Ticks:
    """An injected sleep that returns only when the test releases a tick."""

    def __init__(self) -> None:
        """Start with no tick released."""
        self.calls = 0
        self._gate: asyncio.Queue[None] = asyncio.Queue()

    async def __call__(self, _seconds: float) -> None:
        """Wait for one released tick."""
        self.calls += 1
        await self._gate.get()

    async def tick(self) -> None:
        """Release one tick and let the loop run its refresh."""
        self._gate.put_nowait(None)
        for _ in range(5):
            await asyncio.sleep(0)


@pytest.mark.asyncio
async def test_loop_refreshes_on_each_tick_and_stops_on_exit() -> None:
    """Test the managed-resource lifecycle of the refresh loop.

    **Why this test is important:**
      - Stored policy changes must reach the gateway without a restart, and shutdown must
        not leave a task polling the policy owner.

    **What it tests:**
      - entering refreshes once; each released tick refreshes once more
      - after exit, a released tick refreshes nothing
    """
    loader = AsyncMock(return_value={("org-1", ""): _pinned("a")})
    ticks = _Ticks()
    store = RefreshingPolicyStore(loader, fallback=_FALLBACK, interval_s=30, sleep=ticks)

    async with store:
        assert loader.await_count == 1
        await ticks.tick()
        await ticks.tick()
        assert loader.await_count == _EXPECTED_AWAIT_COUNT

    await ticks.tick()
    assert loader.await_count == _EXPECTED_AWAIT_COUNT


@pytest.mark.asyncio
async def test_startup_survives_a_failing_first_refresh() -> None:
    """Test that a failing first load neither blocks startup nor stops the loop.

    **Why this test is important:**
      - Retrieval must boot while aiops is down, on the config policies, and pick stored
        policies up once aiops is back.

    **What it tests:**
      - entering succeeds when the loader raises; lookups answer from the fallback
      - the next tick loads the stored policy
    """
    loader = AsyncMock(side_effect=[RuntimeError("down"), {("org-3", ""): _pinned("db")}])
    ticks = _Ticks()
    store = RefreshingPolicyStore(loader, fallback=_FALLBACK, interval_s=30, sleep=ticks)

    async with store:
        assert store.policy("org-3", "ws").pin("generate") == "config/model"
        await ticks.tick()
        assert store.policy("org-3", "ws").pin("generate") == "db"
