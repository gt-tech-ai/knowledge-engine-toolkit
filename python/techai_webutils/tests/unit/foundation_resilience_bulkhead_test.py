"""Tests for SemaphoreBulkhead."""

import asyncio

import pytest

from techai_webutils.foundation.resilience.bulkhead import (
    BulkheadFullError,
    SemaphoreBulkhead,
)


class TestSemaphoreBulkhead:
    """Test suite for SemaphoreBulkhead concurrency enforcement."""

    @pytest.mark.asyncio
    async def test_execute_within_limit(self) -> None:
        """Test that execute completes successfully when concurrency is within the limit.

        **Why this test is important:**
          - The bulkhead must allow tasks when slots are available
          - Blocking valid requests under normal load would reduce throughput unnecessarily
          - This validates the happy path before testing rejection behavior

        **What it tests:**
          - Task returns "done" when executed within a bulkhead with max_concurrent=2
        """
        bulkhead = SemaphoreBulkhead(max_concurrent=2)
        result = await bulkhead.execute(self._async_task)
        assert result == "done"

    @pytest.mark.asyncio
    async def test_try_execute_when_available(self) -> None:
        """Test that try_execute succeeds when a semaphore slot is available.

        **Why this test is important:**
          - try_execute is the non-blocking alternative to execute for latency-sensitive paths
          - It must succeed immediately when slots are available without unnecessary waiting
          - Callers use this to implement fast-fail patterns in request handlers

        **What it tests:**
          - Task returns "done" via try_execute when max_concurrent=1 and slot is free
        """
        bulkhead = SemaphoreBulkhead(max_concurrent=1)
        result = await bulkhead.try_execute(self._async_task)
        assert result == "done"

    @pytest.mark.asyncio
    async def test_try_execute_raises_when_full(self) -> None:
        """Test that try_execute raises BulkheadFullError when all slots are occupied.

        **Why this test is important:**
          - Bulkhead rejection is the core protection against resource exhaustion
          - When all slots are occupied, new requests must fail fast to prevent queue buildup
          - The error type (BulkheadFullError) enables callers to return 503 to clients

        **What it tests:**
          - BulkheadFullError is raised when a blocking task holds the only slot
          - The blocking task completes successfully after the error
        """
        bulkhead = SemaphoreBulkhead(max_concurrent=1)
        blocker = asyncio.Event()

        async def blocking_task() -> str:
            await blocker.wait()
            return "blocked"

        # Start a task that holds the slot
        task = asyncio.create_task(bulkhead.execute(blocking_task))
        await asyncio.sleep(0.01)  # Let it acquire the semaphore

        with pytest.raises(BulkheadFullError):
            await bulkhead.try_execute(self._async_task)

        blocker.set()
        await task

    @pytest.mark.asyncio
    async def test_concurrent_execution_limited(self) -> None:
        """Test that the bulkhead never exceeds max_concurrent simultaneous executions.

        **Why this test is important:**
          - The bulkhead's primary purpose is bounding concurrent resource usage
          - Exceeding the limit would defeat the protection and allow resource exhaustion
          - This validates the semaphore correctly serializes excess tasks

        **What it tests:**
          - Peak concurrency across 4 tasks never exceeds max_concurrent=2
        """
        bulkhead = SemaphoreBulkhead(max_concurrent=2)
        counter = {"active": 0, "max": 0}
        gate = asyncio.Event()

        async def tracked_task() -> str:
            counter["active"] += 1
            counter["max"] = max(counter["max"], counter["active"])
            gate.set()
            await asyncio.sleep(0.05)
            counter["active"] -= 1
            return "ok"

        tasks = [asyncio.create_task(bulkhead.execute(tracked_task)) for _ in range(4)]
        await asyncio.gather(*tasks)
        assert counter["max"] <= 2

    @staticmethod
    async def _async_task() -> str:
        return "done"


class TestBulkheadFullError:
    """Test suite for BulkheadFullError error type."""

    def test_default_message(self) -> None:
        """Test that the default error message indicates the bulkhead is full.

        **Why this test is important:**
          - Error messages guide operators during capacity incidents
          - A clear message distinguishes bulkhead rejection from other failures
          - Monitoring systems parse error messages for alerting classification

        **What it tests:**
          - str(err) contains "bulkhead full"
        """
        err = BulkheadFullError()
        assert "bulkhead full" in str(err)

    def test_custom_message(self) -> None:
        """Test that a custom message is preserved in the error string.

        **Why this test is important:**
          - Callers may provide context-specific messages (e.g., service name, endpoint)
          - Custom messages aid debugging by identifying which bulkhead was exhausted
          - The message must not be overwritten by the default

        **What it tests:**
          - str(err) equals the custom message "custom"
        """
        err = BulkheadFullError("custom")
        assert str(err) == "custom"
