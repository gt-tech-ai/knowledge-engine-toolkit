"""Tests for the Ray fan-out width.

In-flight tasks bounded by the cluster's schedulable CPU slots.
"""

from __future__ import annotations

import asyncio
from unittest.mock import AsyncMock, MagicMock, create_autospec, patch

import pytest

from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    executor_from_config,
)
from techai_webutils.execution.executor.ray_executor import RayExecutor
from techai_webutils.execution.executor.ray_runtime import RayRuntime
from techai_webutils.execution.executor.real_ray_runtime import RealRayRuntime
from techai_webutils.execution.executor.resilient_ray_runtime import ResilientRayRuntime

_MODULE = "techai_webutils.execution.executor.real_ray_runtime"


def _runtime(cluster_cpus: float) -> MagicMock:
    """Return a generated-mock ``RayRuntime`` reporting ``cluster_cpus``.

    And running items in-process.

    Returns:
        A generated-mock ``RayRuntime`` reporting ``cluster_cpus``.

    """
    runtime = create_autospec(RayRuntime, instance=True)
    runtime.warm_up = AsyncMock()
    runtime.cluster_cpus = AsyncMock(return_value=cluster_cpus)
    return runtime


async def _mapper(item: int) -> StepResult:
    """Map an item to a named StepResult.

    Returns:
        A passing step for the item.

    """
    await asyncio.sleep(0)
    return StepResult(name=str(item))


class TestRayExecutorWidth:
    """``RayExecutor.width`` and its use by ``run``."""

    @pytest.mark.asyncio
    async def test_width_is_the_cluster_slots_under_the_cap(self) -> None:
        """A 32-CPU cluster with 2-CPU tasks runs 16 at once, under a cap of 64.

        Why this test is important:
          - In-flight tasks beyond the cluster's slots only queue on the head, holding
            driver memory and their inputs while doing nothing.

        What it tests:
          - ``width(64)`` with 32 cluster CPUs and ``task_num_cpus=2`` is 16.
        """
        executor = RayExecutor(_runtime(32.0), task_num_cpus=2.0)

        assert await executor.width(64) == 16

    @pytest.mark.asyncio
    async def test_width_never_drops_below_one(self) -> None:
        """A cluster reporting fewer CPUs than one task needs still runs one task.

        Why this test is important:
          - A cluster scaled to zero workers reports no CPUs; a width of zero would never
            submit the task whose pending demand makes the autoscaler add a worker.

        What it tests:
          - ``width(8)`` with 0 cluster CPUs and ``task_num_cpus=2`` is 1.
        """
        assert await RayExecutor(_runtime(0.0), task_num_cpus=2.0).width(8) == 1

    @pytest.mark.asyncio
    async def test_without_a_task_cost_the_width_is_the_cap(self) -> None:
        """With no declared task CPU cost.

        The width is the cap and the cluster is not asked.

        Why this test is important:
          - The default must keep today's behaviour and cost: the configured concurrency,
            and no extra cluster round trip per batch.

        What it tests:
          - ``width(64)`` with the default ``task_num_cpus`` is 64 and never awaits
            ``cluster_cpus``.
        """
        runtime = _runtime(32.0)

        assert await RayExecutor(runtime).width(64) == 64
        runtime.cluster_cpus.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_run_bounds_in_flight_tasks_by_the_width(self) -> None:
        """``run`` dispatches no more items at once than the cluster has slots.

        Why this test is important:
          - The width only helps if the batch actually honours it.

        What it tests:
          - With 2 cluster CPUs, 1-CPU tasks and a cap of 8, at most 2 of 6 items are in
            flight at any moment, and all 6 complete.
        """
        runtime = _runtime(2.0)
        in_flight = 0
        peak = 0

        async def _submit(_fn: object, item: int) -> StepResult:
            nonlocal in_flight, peak
            in_flight += 1
            peak = max(peak, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            return await _mapper(item)

        runtime.submit = AsyncMock(side_effect=_submit)
        executor = RayExecutor(runtime, task_num_cpus=1.0)

        batch = await executor.run(_mapper, list(range(6)), concurrency=8)

        assert peak == 2
        assert len(batch.results) == 6


class TestRuntimeClusterCpus:
    """The runtimes report the cluster's CPUs and declare each task's CPUs."""

    @pytest.mark.asyncio
    async def test_real_runtime_reports_cluster_cpus_and_declares_task_cpus(self) -> None:
        """The real runtime reads Ray's cluster CPUs and schedules each task with its CPU.

        And memory.

        Why this test is important:
          - Ray places a task by the resources it declares; a task declaring 1 CPU while
            its parse uses 2 oversubscribes every worker.

        What it tests:
          - ``cluster_cpus()`` returns ``ray.cluster_resources()["CPU"]`` (0 when absent),
            and a task is scheduled through ``.options(memory=…, num_cpus=…)``.
        """
        ray = MagicMock()
        ray.is_initialized.return_value = True
        ray.cluster_resources.side_effect = [{"CPU": 24.0, "memory": 1.0}, {}]
        mapper = MagicMock()
        mapper.options.return_value.remote.side_effect = lambda *_: _mapper(1)
        with patch(f"{_MODULE}.ray", ray), patch(f"{_MODULE}._run_mapper", mapper):
            runtime = RealRayRuntime(task_memory_bytes=2 << 30, task_num_cpus=2.0)
            cpus = [await runtime.cluster_cpus(), await runtime.cluster_cpus()]
            await runtime.submit(_mapper, 1)

        assert cpus == [24.0, 0.0]
        mapper.options.assert_called_once_with(memory=2 << 30, num_cpus=2.0)

    @pytest.mark.asyncio
    async def test_resilient_runtime_forwards_cluster_cpus(self) -> None:
        """The resilience decorator connects.

        Then forwards ``cluster_cpus`` to the inner runtime.

        Why this test is important:
          - A decorated runtime that did not forward the new member would break every Ray
            executor built by the factory, which always decorates.

        What it tests:
          - ``ResilientRayRuntime(inner).cluster_cpus()`` warms the inner runtime up and
            returns its value.
        """
        inner = _runtime(12.0)

        assert await ResilientRayRuntime(inner).cluster_cpus() == pytest.approx(12.0)
        inner.warm_up.assert_awaited_once()

    @pytest.mark.asyncio
    async def test_factory_threads_the_task_cpu_cost(self) -> None:
        """``ExecutorConfig.ray_task_num_cpus`` reaches both the executor's width.

        And the task options.

        Why this test is important:
          - The consumer sets one config value; the width and the task's declared CPUs
            must agree, or Ray schedules fewer tasks than the driver keeps in flight.

        What it tests:
          - A ray executor built from config with ``ray_task_num_cpus=2`` over a 32-CPU
            cluster has width 16 under a cap of 64.
        """
        ray = MagicMock()
        ray.is_initialized.return_value = True
        ray.cluster_resources.return_value = {"CPU": 32.0}
        with patch(f"{_MODULE}.ray", ray):
            executor = executor_from_config(
                ExecutorConfig(kind="ray", ray_task_num_cpus=2.0)
            )
            assert isinstance(executor, RayExecutor)
            assert await executor.width(64) == 16
