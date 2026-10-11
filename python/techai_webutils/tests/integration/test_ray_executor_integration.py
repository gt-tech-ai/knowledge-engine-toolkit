"""Integration tests: the Ray executors against a real local Ray cluster.

Unit tests drive the executors through mocks/fakes of their seams; these prove the real paths:

- ``RayExecutor`` + ``RealRayRuntime`` — a mapper is cloudpickled to a Ray worker, run there via
  ``asyncio.run``, and its StepResult returned (the stateless-task path).
- ``PooledExecutor`` + ``real_ray_pool`` — a ``WorkerFactory`` is cloudpickled to a Ray actor that
  builds its worker ONCE and reuses it across the items routed to it (the actor-pool path).

Both fan work across the cluster with the same BatchResult contract as the asyncio path, and a
worker-side exception surfaces as an isolated FAIL (real RayTaskError), not a batch abort.
"""

from __future__ import annotations

import asyncio
import os

import pytest

pytest.importorskip("ray")

# Opt-in gate: ray is importable wherever the dev dependency group is installed (including
# CI), but standing up a real local Ray cluster (``ray.init``) can hang on some machines
# (macOS/Docker), so this module is SKIPPED unless ``RAY_INTEGRATION`` is set — e.g. against
# a reachable Ray cluster. This env guard (not the ``importorskip`` above, which only fires
# where ray is not installed at all) is what keeps the test out of CI and the default local
# run.
if not os.environ.get("RAY_INTEGRATION"):
    pytest.skip(
        "RAY_INTEGRATION not set — real-cluster Ray integration test skipped "
        "(set RAY_INTEGRATION=1 with a reachable Ray cluster to run it)",
        allow_module_level=True,
    )

from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.execution.executor.pooled import PooledExecutor, PooledWorker
from techai_webutils.execution.executor.ray_executor import RayExecutor
from techai_webutils.execution.executor.real_ray_pool import ray_worker_factory
from techai_webutils.execution.executor.real_ray_runtime import RealRayRuntime


async def _pass_even(item: int) -> StepResult:
    """Module-level mapper (picklable for Ray): pass even items, raise on odd ones."""
    await asyncio.sleep(0)
    if item % 2 == 1:
        msg = f"odd item {item}"
        raise ValueError(msg)
    return StepResult(name=f"item-{item}")


class _EvenWorker:
    """PooledWorker (built inside a Ray actor): pass even items, raise on odd ones."""

    async def process(self, item: object) -> StepResult:
        """Pass an even item; raise on an odd one (to prove real per-item isolation)."""
        if isinstance(item, int) and item % 2 == 1:
            msg = f"odd item {item}"
            raise ValueError(msg)
        return StepResult(name=f"item-{item}")

    async def aclose(self) -> None:
        """No resources to release in this fixture worker."""


async def _build_even_worker() -> PooledWorker[int]:
    """Module-level WorkerFactory (picklable for Ray): build one _EvenWorker per actor."""
    await asyncio.sleep(0)
    return _EvenWorker()


@pytest.mark.integration
@pytest.mark.asyncio
@pytest.mark.usefixtures("ray_cluster")
async def test_ray_executor_runs_on_real_cluster() -> None:
    """Test that RayExecutor executes a mapper on a real Ray cluster and isolates worker failures.

    **Why this test is important:**
      - The mock-runtime unit tests can't prove cloudpickling, remote execution, or that a real
        RayTaskError is caught as a single FAIL — only a real cluster does; this is the guarantee
        a large distributed batch relies on.

    **What it tests:**
      - Four items (two even, two odd) yield a BatchResult of two PASS + two FAIL, with the odd
        items' worker-side ValueError isolated (batch not aborted).
    """
    ex = RayExecutor(RealRayRuntime())
    batch = await ex.run(_pass_even, [0, 1, 2, 3], concurrency=2)
    assert batch.total == 4
    assert batch.succeeded == 2
    assert batch.failed == 2


@pytest.mark.integration
@pytest.mark.asyncio
@pytest.mark.usefixtures("ray_cluster")
async def test_pooled_executor_runs_on_real_cluster() -> None:
    """Test that PooledExecutor runs its WorkerFactory as Ray actors and isolates worker failures.

    **Why this test is important:**
      - The fake-worker unit tests can't prove that the ``WorkerFactory`` cloudpickles to a Ray actor,
        that the actor builds the worker once on its own event loop and reuses it, or that a real
        RayTaskError is isolated to one FAIL — only a real cluster does; this is the guarantee the
        actor-pool path relies on.

    **What it tests:**
      - Four items (two even, two odd) fanned over a 2-actor pool yield a BatchResult of two PASS +
        two FAIL, with the odd items' actor-side ValueError isolated (batch not aborted).
    """
    ex = PooledExecutor(ray_worker_factory(_build_even_worker), pool_size=2)
    batch = await ex.run([0, 1, 2, 3], concurrency=2)
    assert batch.total == 4
    assert batch.succeeded == 2
    assert batch.failed == 2
