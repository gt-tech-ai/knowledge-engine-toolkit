"""Tests for RayExecutor (distributed Executor) + the executor_from_config factory.

Ray-free by design: RayExecutor is driven through a generated mock of the ``RayRuntime`` seam, so
these unit tests never import ``ray`` (the real-Ray path is the integration test). RayExecutor must
satisfy the same ``Executor`` protocol as ``AsyncioExecutor`` and inherit ``fan_out``'s
bounded-concurrency + per-item-isolation guarantees, since callers swap the two behind one protocol.
"""

import asyncio
from collections.abc import Awaitable, Callable
from unittest.mock import create_autospec

import pytest

from techai_webutils.core.interfaces.execution import BatchResult, Executor, StepResult
from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor
from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    ExecutorKind,
    executor_from_config,
)
from techai_webutils.execution.executor.ray_executor import RayExecutor
from techai_webutils.execution.executor.ray_runtime import RayRuntime


async def _ok(item: int) -> StepResult:
    """Map ``item`` to a passing step (a trivial always-passing mapper)."""
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


def _local_runtime() -> RayRuntime:
    """Generated-mock RayRuntime whose submit runs the mapper in-process (as Ray would remotely)."""
    runtime = create_autospec(RayRuntime, instance=True)

    async def submit(fn: Callable[[int], Awaitable[StepResult]], item: int) -> StepResult:
        return await fn(item)

    runtime.submit.side_effect = submit
    return runtime


class TestRayExecutor:
    """Tests for the Ray executor."""

    @pytest.mark.asyncio
    async def test_dispatches_every_item(self) -> None:
        """Test that RayExecutor.run submits every item and aggregates a BatchResult.

        **Why this test is important:**
          - A batch caller sees only the Executor; if run() dropped items or skipped a submit, work
            would be silently under-processed on the cluster.

        **What it tests:**
          - Three items under concurrency=2 all pass and each is submitted exactly once.
        """
        runtime = _local_runtime()
        ex = RayExecutor(runtime)
        batch = await ex.run(_ok, [1, 2, 3], concurrency=2)
        assert isinstance(batch, BatchResult)
        assert batch.total == 3
        assert batch.succeeded == 3
        assert runtime.submit.await_count == 3

    @pytest.mark.asyncio
    async def test_isolates_a_failing_task(self) -> None:
        """Test that a Ray task error for one item is isolated to a single FAIL.

        **Why this test is important:**
          - A remote worker crash (RayTaskError) must not abort the batch; one poison document
            failing the whole batch would defeat partial-failure tolerance.

        **What it tests:**
          - When submit raises for one item, that item FAILs and the rest PASS (fan_out isolation).
        """
        runtime = create_autospec(RayRuntime, instance=True)

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            if item == 1:
                msg = "ray task died"
                raise RuntimeError(msg)
            return await fn(item)

        runtime.submit.side_effect = submit
        ex = RayExecutor(runtime)
        batch = await ex.run(_ok, [0, 1, 2], concurrency=3)
        assert batch.failed == 1
        assert batch.succeeded == 2

    @pytest.mark.asyncio
    async def test_bounds_concurrency(self) -> None:
        """Test that RayExecutor never has more than ``concurrency`` submits in flight.

        **Why this test is important:**
          - The concurrency bound is what keeps a large batch from flooding the Ray cluster (and any
            downstream service) with unbounded in-flight tasks.

        **What it tests:**
          - With concurrency=2 over 6 items, peak simultaneous submits never exceeds 2.
        """
        in_flight = 0
        peak = 0

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            nonlocal in_flight, peak
            in_flight += 1
            peak = max(peak, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            return await fn(item)

        runtime = create_autospec(RayRuntime, instance=True)
        runtime.submit.side_effect = submit
        ex = RayExecutor(runtime)
        await ex.run(_ok, list(range(6)), concurrency=2)
        assert peak <= 2

    @pytest.mark.asyncio
    async def test_satisfies_executor_protocol(self) -> None:
        """Test that RayExecutor is usable wherever the Executor protocol is expected.

        **Why this test is important:**
          - executor_from_config returns Executor; if RayExecutor's signature diverged, the swap
            with AsyncioExecutor would break at runtime, not here.

        **What it tests:**
          - Passing RayExecutor to a function typed on Executor and calling run() works.
        """

        async def use(ex: Executor) -> BatchResult:
            return await ex.run(_ok, [1], concurrency=1)

        batch = await use(RayExecutor(_local_runtime()))
        assert batch.total == 1


class TestExecutorFromConfig:
    """Tests for ``executor_from_config``."""

    def test_asyncio_kind_builds_in_process_executor(self) -> None:
        """Test that the asyncio kind selects the in-process AsyncioExecutor.

        **Why this test is important:**
          - asyncio is the dev/per-message default; a misroute here would pull Ray into every
            environment (the whole point of the config seam is that it does not).

        **What it tests:**
          - executor_from_config(kind=asyncio) returns an AsyncioExecutor.
        """
        ex = executor_from_config(ExecutorConfig(kind=ExecutorKind.ASYNCIO))
        assert isinstance(ex, AsyncioExecutor)

    def test_ray_kind_builds_ray_executor(self) -> None:
        """Test that the ray kind selects the distributed RayExecutor.

        **Why this test is important:**
          - This is the top-level-config swap to distributed execution; it must wire a RayExecutor
            (backed by a RealRayRuntime) without the caller changing any mapper code.

        **What it tests:**
          - executor_from_config(kind=ray) returns a RayExecutor (skipped if the ray extra is absent).
        """
        pytest.importorskip("ray")
        ex = executor_from_config(ExecutorConfig(kind=ExecutorKind.RAY, ray_address=""))
        assert isinstance(ex, RayExecutor)

    def test_unknown_kind_raises(self) -> None:
        """Test that an unrecognized executor kind fails loudly.

        **Why this test is important:**
          - A typo in ``ingestion.executor.kind`` must fail fast at composition, not silently fall
            back to the wrong executor (mirrors logger.NewFromConfig).

        **What it tests:**
          - executor_from_config with an unknown kind raises ValueError naming the bad kind.
        """
        with pytest.raises(ValueError, match="unknown executor kind"):
            executor_from_config(ExecutorConfig(kind="bogus"))
