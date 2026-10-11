"""Tests for PooledExecutor: build a per-worker context once, route items across a fixed pool.

Ray-free by design: PooledExecutor is the driver-side pool (build-once + round-robin routing) that
the Ray actor pool plugs into via the PooledWorker seam, so these unit tests drive it with a
pure-Python counting worker — the real Ray-actor behaviour is the integration test. PooledExecutor
must inherit fan_out's bounded-concurrency + per-item-isolation guarantees while building each
worker's (expensive) context exactly once and closing every built worker at the end.
"""

import asyncio
from dataclasses import dataclass, field

import pytest

from techai_webutils.core.interfaces.execution import StepResult
from techai_webutils.execution.executor.pooled import (
    PooledExecutor,
    PooledWorker,
)


@dataclass
class _Log:
    """Shared tracker: how many workers were built/closed and every item processed across them."""

    built: int = 0
    closed: int = 0
    processed: list[int] = field(default_factory=list)


class _CountingWorker:
    """PooledWorker that records its own build + every item it processes into a shared log."""

    def __init__(self, log: _Log) -> None:
        """Register this build in the shared log so the test can count workers actually created."""
        self._log = log
        log.built += 1

    async def process(self, item: int) -> StepResult:
        """Record the item and return a passing StepResult."""
        self._log.processed.append(item)
        return StepResult(name=f"item-{item}")

    async def aclose(self) -> None:
        """Record that this worker was closed (the pool must close every built worker)."""
        self._log.closed += 1


def _counting_factory(log: _Log):  # noqa: ANN202
    """Return a WorkerFactory that builds counting workers recording into ``log``."""

    async def build() -> PooledWorker[int]:
        await asyncio.sleep(0)
        return _CountingWorker(log)

    return build


class TestPooledExecutor:
    """Tests for the pooled executor."""

    @pytest.mark.asyncio
    async def test_builds_each_worker_once_and_routes_all(self) -> None:
        """Test that the pool builds one worker per slot, reuses it, and closes them all.

        **Why this test is important:**
          - Building the per-worker context once and reusing it across items is the whole point of
            the pool (the per-object client rebuild is the cost being removed); a rebuild
            per item, or a leaked (unclosed) worker, would defeat it.

        **What it tests:**
          - 10 items over a pool of 3 build exactly 3 workers, process all 10 items, and close all 3.
        """
        log = _Log()
        ex = PooledExecutor(_counting_factory(log), pool_size=3)

        batch = await ex.run(list(range(10)), concurrency=3)

        assert batch.total == 10
        assert batch.succeeded == 10
        assert log.built == 3
        assert log.closed == 3
        assert sorted(log.processed) == list(range(10))

    @pytest.mark.asyncio
    async def test_slot_builds_once_under_concurrent_first_calls(self) -> None:
        """Test that a pool slot builds its worker EXACTLY once even under concurrent first calls.

        **Why this test is important:**
          - The slot's build-once primitive is shared by both pool paths; it is load-bearing
            in the Ray actor (real_ray_pool._WorkerActor), where an async actor runs its methods
            concurrently on one event loop. Without the lock, concurrent first-item calls each
            ``await factory()`` (the build yields on the S3-session open) and build a SECOND worker,
            orphaning the first's un-closed clients and defeating the reuse — the guarantee the Ray
            actor pool relies on. Ray-free so the guarantee runs in CI.

        **What it tests:**
          - 8 items dispatched concurrently onto a one-slot pool, whose factory yields mid-build,
            invoke the factory exactly once; that one worker processes all 8 and is closed once.
        """
        log = _Log()
        builds = 0

        async def factory() -> PooledWorker[int]:
            nonlocal builds
            builds += 1
            # Yield DURING the build so concurrent first calls interleave — the exact race window a
            # missing build-once lock would let through (each would build its own worker).
            await asyncio.sleep(0)
            return _CountingWorker(log)

        batch = await PooledExecutor(factory, pool_size=1).run(
            list(range(8)), concurrency=8
        )

        assert builds == 1  # built exactly once despite 8 concurrent first calls
        assert batch.succeeded == 8
        assert sorted(log.processed) == list(range(8))  # the one worker served every item
        assert log.closed == 1

    @pytest.mark.asyncio
    async def test_isolates_a_failing_item(self) -> None:
        """Test that one worker.process exception becomes a single FAIL, not a batch abort.

        **Why this test is important:**
          - Per-item isolation is inherited from fan_out; if PooledExecutor's routing swallowed or
            propagated the error differently, one poison object would abort the whole batch.

        **What it tests:**
          - Given one raising item out of four, the batch is 3 succeeded + 1 failed.
        """

        class _Worker:
            async def process(self, item: object) -> StepResult:
                if item == 2:
                    msg = "boom"
                    raise ValueError(msg)
                return StepResult(name=f"item-{item}")

            async def aclose(self) -> None: ...

        async def factory() -> PooledWorker[int]:
            await asyncio.sleep(0)
            return _Worker()

        ex = PooledExecutor(factory, pool_size=2)

        batch = await ex.run([0, 1, 2, 3], concurrency=2)

        assert batch.failed == 1
        assert batch.succeeded == 3

    @pytest.mark.asyncio
    async def test_bounds_concurrency(self) -> None:
        """Test that the pool never has more than ``concurrency`` items in flight at once.

        **Why this test is important:**
          - The concurrency bound is what keeps the actor pool (and the downstream KB) from being
            flooded; PooledExecutor must not widen it while routing.

        **What it tests:**
          - With concurrency=2 over 8 items, the peak simultaneous in-flight process() calls is <= 2.
        """
        in_flight = 0
        peak = 0

        class _Worker:
            async def process(self, item: object) -> StepResult:
                nonlocal in_flight, peak
                in_flight += 1
                peak = max(peak, in_flight)
                await asyncio.sleep(0.01)
                in_flight -= 1
                return StepResult(name=f"item-{item}")

            async def aclose(self) -> None: ...

        async def factory() -> PooledWorker[int]:
            await asyncio.sleep(0)
            return _Worker()

        ex = PooledExecutor(factory, pool_size=4)

        await ex.run(list(range(8)), concurrency=2)

        assert peak <= 2
