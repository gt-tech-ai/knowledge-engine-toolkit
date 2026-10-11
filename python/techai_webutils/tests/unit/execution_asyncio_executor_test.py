"""Tests for AsyncioExecutor (the in-process Executor backed by fan_out)."""

import asyncio

import pytest

from techai_webutils.core.interfaces.execution import BatchResult, Executor, StepResult
from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor


async def _ok(item: int) -> StepResult:
    """Map ``item`` to a passing step (a trivial always-passing mapper)."""
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


class TestAsyncioExecutor:
    """Tests for the asyncio executor."""

    @pytest.mark.asyncio
    async def test_runs_all_items_bounded(self) -> None:
        """Test that AsyncioExecutor.run fans every item out and returns a BatchResult.

        **Why this test is important:**
          - The Executor is the seam every batch caller uses; if run() dropped items or
            returned the wrong type, callers would silently under-process.

        **What it tests:**
          - Running three items under concurrency=2 returns a BatchResult with all three passing.
        """
        ex = AsyncioExecutor()
        batch = await ex.run(_ok, [1, 2, 3], concurrency=2)
        assert isinstance(batch, BatchResult)
        assert batch.total == 3
        assert batch.succeeded == 3

    @pytest.mark.asyncio
    async def test_isolates_failure_like_fan_out(self) -> None:
        """Test that AsyncioExecutor inherits fan_out's per-item failure isolation.

        **Why this test is important:**
          - The Executor must not lose fan_out's isolation guarantee; one bad item aborting the
            batch would defeat partial-failure tolerance for callers that only see the Executor.

        **What it tests:**
          - One raising item yields one FAIL and the rest PASS.
        """

        async def fn(item: int) -> StepResult:
            await asyncio.sleep(0)
            if item == 1:
                msg = "x"
                raise ValueError(msg)
            return StepResult(name=str(item))

        ex = AsyncioExecutor()
        batch = await ex.run(fn, [0, 1, 2], concurrency=3)
        assert batch.failed == 1
        assert batch.succeeded == 2

    @pytest.mark.asyncio
    async def test_satisfies_executor_protocol(self) -> None:
        """Test that AsyncioExecutor is usable wherever the Executor protocol is expected.

        **Why this test is important:**
          - RayExecutor must be swappable for AsyncioExecutor behind one protocol; this
            pins the structural contract so a divergent signature is caught here, not at swap time.

        **What it tests:**
          - Passing AsyncioExecutor to a function typed on Executor and calling run() works.
        """

        async def use(ex: Executor) -> BatchResult:
            return await ex.run(_ok, [1], concurrency=1)

        batch = await use(AsyncioExecutor())
        assert batch.total == 1
