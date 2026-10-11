"""Tests for the async bounded-concurrency fan-out engine."""

import asyncio
from typing import override

import pytest

from techai_webutils.core.interfaces.execution import (
    BatchResult,
    NopObserver,
    StepResult,
    StepStatus,
)
from techai_webutils.execution.engine.fan_out import fan_out


async def _ok(item: int) -> StepResult:
    """Map ``item`` to a passing step (the observer/empty tests' trivial mapper)."""
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


class TestFanOut:
    """Tests for ``fan_out``."""

    @pytest.mark.asyncio
    async def test_bounds_concurrency(self) -> None:
        """Test that fan_out never runs more than `concurrency` mappers at once.

        **Why this test is important:**
          - The whole point of the engine is *bounded* fan-out; an unbounded burst would
            OOM the ingestion pod (N parses x 512Mi) — the concurrency cap is a hard safety limit.

        **What it tests:**
          - With concurrency=3 over 12 items, the peak observed in-flight count is <= 3 and all
            12 complete.
        """
        in_flight = 0
        max_seen = 0

        async def fn(item: int) -> StepResult:
            nonlocal in_flight, max_seen
            in_flight += 1
            max_seen = max(max_seen, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            return StepResult(name=f"item-{item}")

        batch = await fan_out(list(range(12)), 3, fn)
        assert batch.total == 12
        assert batch.succeeded == 12
        assert max_seen <= 3

    @pytest.mark.asyncio
    async def test_large_batch_is_windowed_and_ordered(self) -> None:
        """Test a batch far larger than the concurrency limit stays bounded and returns ordered results.

        **Why this test is important:**
          - A large batch can fan tens of thousands of objects through one fan_out; the fixed worker pool
            must keep at most `concurrency` mappers in flight for a huge batch (bounding
            driver memory to O(concurrency), not O(batch)) WITHOUT reordering the per-item results.

        **What it tests:**
          - Over 500 items with concurrency=4: the peak in-flight count is <= 4, all 500 succeed, and
            each result lands at its item index (result i names item-i).
        """
        in_flight = 0
        max_seen = 0

        async def fn(item: int) -> StepResult:
            nonlocal in_flight, max_seen
            in_flight += 1
            max_seen = max(max_seen, in_flight)
            await asyncio.sleep(0)
            in_flight -= 1
            return StepResult(name=f"item-{item}")

        batch = await fan_out(list(range(500)), 4, fn)
        assert batch.total == 500
        assert batch.succeeded == 500
        assert max_seen <= 4
        assert [r.name for r in batch.results] == [f"item-{i}" for i in range(500)]

    @pytest.mark.asyncio
    async def test_isolates_one_failing_item(self) -> None:
        """Test that a single mapper exception becomes a FAIL result without aborting the batch.

        **Why this test is important:**
          - Partial-failure tolerance is the resiliency core: one poison document must fail alone
            (captured with its traceback) while the other nine in the poll still succeed.

        **What it tests:**
          - One raising item yields exactly one FAIL (carrying the message + traceback in detail),
            the other four PASS, and the batch still reports all five.
        """

        async def fn(item: int) -> StepResult:
            await asyncio.sleep(0)
            if item == 2:
                msg = "boom"
                raise ValueError(msg)
            return StepResult(name=f"item-{item}")

        batch = await fan_out(list(range(5)), 2, fn)
        assert batch.total == 5
        assert batch.succeeded == 4
        assert batch.failed == 1
        failed = next(r for r in batch.results if r.status is StepStatus.FAIL)
        assert "boom" in failed.error
        assert "ValueError" in failed.detail

    @pytest.mark.asyncio
    async def test_cancelled_error_propagates_not_swallowed(self) -> None:
        """Test that asyncio.CancelledError from a mapper propagates instead of becoming a FAIL.

        **Why this test is important:**
          - CancelledError is a BaseException; catching it as a per-item failure would silently
            swallow shutdown/cancellation and break structured cancellation of the whole service.

        **What it tests:**
          - A mapper raising CancelledError causes fan_out to raise CancelledError (not return a
            FAIL result).
        """

        async def fn(item: int) -> StepResult:
            if item == 1:
                raise asyncio.CancelledError
            await asyncio.sleep(0.01)
            return StepResult(name=f"item-{item}")

        with pytest.raises(asyncio.CancelledError):
            await fan_out(list(range(4)), 4, fn)

    @pytest.mark.asyncio
    async def test_observer_receives_exactly_one_complete_per_item(self) -> None:
        """Test that the observer sees one batch-start, one step-complete per item, one batch-complete.

        **Why this test is important:**
          - The observer is the *sole* metric emitter; a double-fire or miss corrupts
            documents_failed_total / inflight — exactly-once delivery is the metric-correctness guarantee.

        **What it tests:**
          - on_batch_start carries the item count, on_step_complete fires exactly len(items) times,
            and on_batch_complete carries the full aggregate.
        """
        completed: list[StepResult] = []
        started: list[int] = []
        finished: list[BatchResult] = []

        class Obs(NopObserver):
            @override
            def on_batch_start(self, name: str, total: int) -> None:
                started.append(total)

            @override
            def on_step_complete(self, result: StepResult) -> None:
                completed.append(result)

            @override
            def on_batch_complete(
                self, name: str, result: BatchResult, elapsed: float
            ) -> None:
                finished.append(result)

        batch = await fan_out(list(range(7)), 3, _ok, Obs())
        assert started == [7]
        assert len(completed) == 7
        assert finished[0].total == 7
        assert batch.total == 7

    @pytest.mark.asyncio
    async def test_empty_batch_returns_empty_result(self) -> None:
        """Test that an empty item list returns an empty, vacuously-passed BatchResult.

        **Why this test is important:**
          - An empty SQS poll is the steady-state idle case; it must return cleanly (no failure,
            no crash) rather than divide-by-zero or mis-signal.

        **What it tests:**
          - fan_out([], ...) returns total 0 and all_passed True.
        """
        batch = await fan_out([], 4, _ok)
        assert batch.total == 0
        assert batch.all_passed is True
