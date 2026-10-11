"""Tests for ResilientRayRuntime — the retry-with-backoff decorator over the RayRuntime seam.

Ray-free by design: ResilientRayRuntime wraps a generated mock of the ``RayRuntime`` seam, so these
unit tests never import ``ray`` (the real cold-connect flakiness is a staging concern, not a unit one).
The decorator's whole job is to turn a flaky cold connect into a reliable one and keep the task path
untouched, so the tests assert exactly that: the connect retries, an exhausted connect surfaces a
transient error, and ``submit`` connects-then-dispatches without retrying the task itself.
"""

import asyncio
from collections.abc import Awaitable, Callable
from typing import TypedDict
from unittest.mock import create_autospec

import pytest

from techai_webutils.core.errors.errors import ErrorCode, UnavailableError
from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.execution.executor.ray_runtime import RayRuntime
from techai_webutils.execution.executor.resilient_ray_runtime import ResilientRayRuntime


# Near-zero backoff so the retry tests do not actually sleep through the exponential waits.
class _RetryOptions(TypedDict):
    """The ResilientRayRuntime retry options the tests pass."""

    max_attempts: int
    """Connect attempts before giving up."""
    base_delay: float
    """First backoff delay in seconds."""
    max_delay: float
    """Backoff ceiling in seconds."""


_FAST: _RetryOptions = {"max_attempts": 5, "base_delay": 0.0, "max_delay": 0.0}


async def _ok(item: int) -> StepResult:
    """Map ``item`` to a passing step (a trivial always-passing mapper)."""
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


class TestResilientRayRuntime:
    """Tests for the resilient Ray runtime."""

    @pytest.mark.asyncio
    async def test_warm_up_retries_a_flaky_cold_connect(self) -> None:
        """Test that warm_up retries a transient cold-connect failure until it succeeds.

        **Why this test is important:**
          - The head's per-connection Ray Client server intermittently times out on a cold connect; if
            warm_up did not retry, a pod's first batch would fail — the exact symptom this decorator
            exists to remove.

        **What it tests:**
          - With the inner runtime failing the first two connects and succeeding on the third, warm_up
            returns without raising and the inner connect was attempted exactly three times.
        """
        inner = create_autospec(RayRuntime, instance=True)
        inner.warm_up.side_effect = [
            RuntimeError("Starting Ray client server failed"),
            RuntimeError("Starting Ray client server failed"),
            None,
        ]
        runtime = ResilientRayRuntime(inner, **_FAST)

        await runtime.warm_up()

        assert inner.warm_up.await_count == 3

    @pytest.mark.asyncio
    async def test_warm_up_raises_transient_after_exhausting_retries(self) -> None:
        """Test that an always-failing cold connect surfaces a transient UnavailableError.

        **Why this test is important:**
          - When Ray really is unreachable the caller must get a *transient* error (so the batch is
            redriven, not dead-lettered) with an error code, not a raw RuntimeError — the coded-error
            contract (ARCHITECTURE.md#error-codes).

        **What it tests:**
          - With the inner connect always failing, warm_up raises UnavailableError (code UNAVAILABLE,
            is_transient) after exactly ``max_attempts`` attempts.
        """
        inner = create_autospec(RayRuntime, instance=True)
        inner.warm_up.side_effect = RuntimeError("Starting Ray client server failed")
        runtime = ResilientRayRuntime(
            inner, max_attempts=3, base_delay=0.0, max_delay=0.0
        )

        with pytest.raises(UnavailableError) as excinfo:
            await runtime.warm_up()

        assert excinfo.value.code is ErrorCode.UNAVAILABLE
        assert excinfo.value.is_transient
        assert inner.warm_up.await_count == 3

    @pytest.mark.asyncio
    async def test_submit_connects_then_dispatches_without_retrying_the_task(
        self,
    ) -> None:
        """Test that submit establishes the connection (retried) then dispatches, leaving the task alone.

        **Why this test is important:**
          - Retrying the *task* here would double-retry against fan_out's per-item isolation; only the
            connect is the decorator's concern. submit must still connect through the resilient path so a
            batch that arrives before the startup warm-up is covered.

        **What it tests:**
          - submit connects via the inner runtime, dispatches the item exactly once, and returns the
            mapper's StepResult.
        """
        inner = create_autospec(RayRuntime, instance=True)
        inner.warm_up.side_effect = [None]

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            return await fn(item)

        inner.submit.side_effect = submit
        runtime = ResilientRayRuntime(inner, **_FAST)

        result = await runtime.submit(_ok, 5)

        assert result.name == "item-5"
        inner.warm_up.assert_awaited()
        inner.submit.assert_awaited_once()
