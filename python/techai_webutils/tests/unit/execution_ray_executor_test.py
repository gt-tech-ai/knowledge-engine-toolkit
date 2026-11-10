"""Tests for RayExecutor (distributed Executor) + the executor_from_config factory.

Ray-free by design: RayExecutor is driven through a generated mock of the ``RayRuntime``
seam, so these unit tests never import ``ray`` (the real-Ray path is the integration
test). RayExecutor must satisfy the same ``Executor`` protocol as ``AsyncioExecutor`` and
inherit ``fan_out``'s bounded-concurrency + per-item-isolation guarantees, since callers
swap the two behind one protocol.
"""

import asyncio
from collections.abc import Awaitable, Callable
from typing import cast
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.core.interfaces.execution import BatchResult, Executor, StepResult
from techai_webutils.execution.executor.asyncio_executor import AsyncioExecutor
from techai_webutils.execution.executor.factory import (
    ExecutorConfig,
    ExecutorKind,
    executor_from_config,
)
from techai_webutils.execution.executor.ray_executor import RayExecutor
from techai_webutils.execution.executor.ray_runtime import RayRuntime, RayUnavailableError

_EXPECTED_TOTAL = 3
_EXPECTED_SUCCEEDED = 3
_EXPECTED_AWAIT_COUNT = 3
_EXPECTED_SUCCEEDED_2 = 2
_EXPECTED_AWAIT_COUNT_2 = 2
_EXPECTED_PEAK = 2


async def _ok(item: int) -> StepResult:
    """Map ``item`` to a passing step (a trivial always-passing mapper).

    Args:
        item: The item.

    Returns:
        A passing step for the item.

    """
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


def _local_runtime() -> RayRuntime:
    """Generated-mock RayRuntime whose submit runs the mapper in-process.

    (as Ray would remotely).

    Returns:
        The mock runtime.

    """
    runtime = cast("object", create_autospec(RayRuntime, instance=True))

    async def submit(fn: Callable[[int], Awaitable[StepResult]], item: int) -> StepResult:
        return await fn(item)

    cast("MagicMock", runtime.submit).side_effect = submit
    return cast("RayRuntime", runtime)


class TestRayExecutor:
    """Tests for the Ray executor."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_dispatches_every_item() -> None:
        """Test that RayExecutor.run submits every item and aggregates a BatchResult.

        **Why this test is important:**
          - A batch caller sees only the Executor; if run() dropped items or skipped a
            submit, work would be silently under-processed on the cluster.

        **What it tests:**
          - Three items under concurrency=2 all pass and each is submitted exactly once.
        """
        runtime = _local_runtime()
        ex = RayExecutor(runtime)
        batch = await ex.run(_ok, [1, 2, 3], concurrency=2)
        assert isinstance(batch, BatchResult)
        assert batch.total == _EXPECTED_TOTAL
        assert batch.succeeded == _EXPECTED_SUCCEEDED
        assert runtime.submit.await_count == _EXPECTED_AWAIT_COUNT

    @staticmethod
    @pytest.mark.asyncio
    async def test_isolates_a_failing_task() -> None:
        """Test that a Ray task error for one item is isolated to a single FAIL.

        **Why this test is important:**
          - A remote worker crash (RayTaskError) must not abort the batch; one poison
            document failing the whole batch would defeat partial-failure tolerance.

        **What it tests:**
          - When submit raises for one item, that item FAILs and the rest PASS (fan_out
            isolation).
        """
        runtime = cast("object", create_autospec(RayRuntime, instance=True))

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            """Fail item 1 as a dead task; run every other.

            Args:
                fn: The mapper.
                item: The work item.

            Returns:
                The item's result.

            Raises:
                RuntimeError: For item 1.

            """
            if item == 1:
                msg = "ray task died"
                raise RuntimeError(msg)
            return await fn(item)

        cast("MagicMock", runtime.submit).side_effect = submit
        ex = RayExecutor(cast("RayRuntime", runtime))
        batch = await ex.run(_ok, [0, 1, 2], concurrency=3)
        assert batch.failed == 1
        assert batch.succeeded == _EXPECTED_SUCCEEDED_2

    @staticmethod
    @pytest.mark.asyncio
    async def test_unreachable_cluster_raises_instead_of_failing_every_item() -> None:
        """Test that a cluster.

        That cannot be reached aborts the batch rather than failing each item.

        **Why this test is important:**
          - When the connect failed per item, the batch "completed" with every item FAILed
            and its queue message was acknowledged — the work was silently dropped instead
            of redriven.

        **What it tests:**
          - With ``warm_up`` raising ``RayUnavailableError``, ``run`` raises it and
            dispatches no item.
        """
        runtime = _local_runtime()
        runtime.warm_up.side_effect = RayUnavailableError("ray cluster connect failed")
        ex = RayExecutor(runtime)

        with pytest.raises(RayUnavailableError):
            _ = await ex.run(_ok, [0, 1, 2], concurrency=3)
        runtime.submit.assert_not_awaited()

    @staticmethod
    @pytest.mark.asyncio
    async def test_cluster_lost_mid_batch_stops_dispatch_and_raises() -> None:
        """Test that losing the cluster mid-batch stops dispatching and raises.

        For the whole batch.

        **Why this test is important:**
          - A head that dies mid-batch makes every later item fail the same way;
            dispatching them all just to record N identical FAILs wastes the retry budget
            and still drops the batch.

        **What it tests:**
          - Serially over five items, item 1's submit raises ``RayUnavailableError``:
            ``run`` raises it and items 2-4 are never submitted.
        """
        runtime = _local_runtime()

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            """Lose the cluster on item 1; run every other.

            Args:
                fn: The mapper.
                item: The work item.

            Returns:
                The item's result.

            Raises:
                RayUnavailableError: For item 1.

            """
            if item == 1:
                msg = "ray cluster connect failed"
                raise RayUnavailableError(msg)
            return await fn(item)

        runtime.submit.side_effect = submit
        ex = RayExecutor(runtime)

        with pytest.raises(RayUnavailableError):
            _ = await ex.run(_ok, [0, 1, 2, 3, 4], concurrency=1)
        assert runtime.submit.await_count == _EXPECTED_AWAIT_COUNT_2

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_mapper_unavailable_error_stays_a_per_item_failure() -> None:
        """Test that an item's own UnavailableError.

        (a downstream outage) is still isolated to that item.

        **Why this test is important:**
          - A mapper's dependency outage reaches the driver as an ``UnavailableError``;
            treating it as a cluster outage would abort and redrive whole batches for one
            item's flaky S3 read.

        **What it tests:**
          - A plain ``UnavailableError`` from one item's submit yields one FAIL and two
            PASS; no raise.
        """
        runtime = _local_runtime()

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            """Fail item 1 in the mapper; run every other.

            Args:
                fn: The mapper.
                item: The work item.

            Returns:
                The item's result.

            Raises:
                UnavailableError: For item 1.

            """
            if item == 1:
                msg = "s3 unavailable"
                raise UnavailableError(msg)
            return await fn(item)

        runtime.submit.side_effect = submit
        batch = await RayExecutor(runtime).run(_ok, [0, 1, 2], concurrency=3)

        assert batch.failed == 1
        assert batch.succeeded == _EXPECTED_SUCCEEDED_2

    @staticmethod
    @pytest.mark.asyncio
    async def test_bounds_concurrency() -> None:
        """Test that RayExecutor never has more than ``concurrency`` submits in flight.

        **Why this test is important:**
          - The concurrency bound is what keeps a large batch from flooding the Ray
            cluster (and any downstream service) with unbounded in-flight tasks.

        **What it tests:**
          - With concurrency=2 over 6 items, peak simultaneous submits never exceeds 2.
        """
        in_flight = 0
        peak = 0

        async def submit(
            fn: Callable[[int], Awaitable[StepResult]], item: int
        ) -> StepResult:
            """Run the item, tracking how many run at once.

            Args:
                fn: The mapper.
                item: The work item.

            Returns:
                The item's result.

            """
            nonlocal in_flight, peak
            in_flight += 1
            peak = max(peak, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            return await fn(item)

        runtime = cast("object", create_autospec(RayRuntime, instance=True))
        cast("MagicMock", runtime.submit).side_effect = submit
        ex = RayExecutor(cast("RayRuntime", runtime))
        _ = await ex.run(_ok, list(range(6)), concurrency=2)
        assert peak <= _EXPECTED_PEAK

    @staticmethod
    @pytest.mark.asyncio
    async def test_satisfies_executor_protocol() -> None:
        """Test that RayExecutor is usable wherever the Executor protocol is expected.

        **Why this test is important:**
          - executor_from_config returns Executor; if RayExecutor's signature diverged,
            the swap with AsyncioExecutor would break at runtime, not here.

        **What it tests:**
          - Passing RayExecutor to a function typed on Executor and calling run() works.
        """

        async def use(ex: Executor) -> BatchResult:
            """Run one item through any ``Executor``.

            Args:
                ex: The executor.

            Returns:
                The batch result.

            """
            return await ex.run(_ok, [1], concurrency=1)

        batch = await use(RayExecutor(_local_runtime()))
        assert batch.total == 1


class TestExecutorFromConfig:
    """Tests for ``executor_from_config``."""

    @staticmethod
    def test_asyncio_kind_builds_in_process_executor() -> None:
        """Test that the asyncio kind selects the in-process AsyncioExecutor.

        **Why this test is important:**
          - asyncio is the dev/per-message default; a misroute here would pull Ray into
            every environment (the whole point of the config seam is that it does not).

        **What it tests:**
          - executor_from_config(kind=asyncio) returns an AsyncioExecutor.
        """
        ex = executor_from_config(ExecutorConfig(kind=ExecutorKind.ASYNCIO))
        assert isinstance(ex, AsyncioExecutor)

    @staticmethod
    def test_ray_kind_builds_ray_executor() -> None:
        """Test that the ray kind selects the distributed RayExecutor.

        **Why this test is important:**
          - This is the top-level-config swap to distributed execution; it must wire a
            RayExecutor (backed by a RealRayRuntime) without the caller changing any
            mapper code.

        **What it tests:**
          - executor_from_config(kind=ray) returns a RayExecutor (skipped if the ray extra
            is absent).
        """
        pytest.importorskip("ray")
        ex = executor_from_config(ExecutorConfig(kind=ExecutorKind.RAY, ray_address=""))
        assert isinstance(ex, RayExecutor)

    @staticmethod
    def test_unknown_kind_raises() -> None:
        """Test that an unrecognized executor kind fails loudly.

        **Why this test is important:**
          - A typo in ``ingestion.executor.kind`` must fail fast at composition, not
            silently fall back to the wrong executor (mirrors logger.NewFromConfig).

        **What it tests:**
          - executor_from_config with an unknown kind raises ValueError naming the bad
            kind.
        """
        with pytest.raises(AppValueError, match="unknown executor kind"):
            _ = executor_from_config(ExecutorConfig(kind="bogus"))
