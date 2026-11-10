"""Tests for the Job[T] discover -> map -> fan-out builder."""

import asyncio
from typing import cast
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.core.interfaces.execution import (
    AnyJob,
    Discoverer,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.execution.job.job import JobBuilder

_EXPECTED_TOTAL = 3
_EXPECTED_SUCCEEDED = 3
_EXPECTED_CALLS = 2
_EXPECTED_WARNED = 2


async def _ok(item: int) -> StepResult:
    """Process ``item`` into a passing step (a trivial always-passing processor).

    Returns:
        A passing step for the item.

    """
    await asyncio.sleep(0)
    return StepResult(name=f"item-{item}")


def _discoverer(items: list[int]) -> Discoverer[int]:
    """Generated-mock Discoverer[T] whose discover() returns a fixed item list.

    Configures the autospec's AsyncMock child in place (rather than replacing it) so the
    port's ``discover`` signature stays enforced — consistent with the processor-port
    mocks.

    Returns:
        The mock discoverer.

    """
    disc = cast("object", create_autospec(Discoverer, instance=True))
    cast("MagicMock", disc.discover).return_value = items
    return cast("Discoverer[int]", disc)


class TestJob:
    """Tests for the job."""

    @staticmethod
    def test_built_job_is_anyjob_with_meta() -> None:
        """Test that a built.

        Grouped Job satisfies AnyJob and reports its name + group via meta().

        **Why this test is important:**
          - The gate composes AnyJobs and labels each by meta(); a Job that didn't expose
            meta() couldn't be composed into a gate or attributed in metrics/logs.

        **What it tests:**
          - A grouped, built Job is an AnyJob and meta() carries the configured name +
            group.
        """
        job = (
            JobBuilder[int]("ingest")
            .grouped("worker")
            .with_discoverer(_discoverer([]))
            .with_processor(_ok)
            .build()
        )
        assert isinstance(job, AnyJob)
        assert job.meta() == JobMeta(name="ingest", group="worker")

    @staticmethod
    @pytest.mark.asyncio
    async def test_empty_discovery_returns_skip() -> None:
        """Test that discovering no items yields a single SKIP result.

        (mirrors Go Job.Execute).

        **Why this test is important:**
          - An idle poll (no work) must read as "skipped", not a failure or a crash, so a
            job wired into a gate/loop stays green when there's nothing to do.

        **What it tests:**
          - Empty discovery returns exactly one SKIP result named for the job.
        """
        job = (
            JobBuilder[int]("ingest")
            .with_discoverer(_discoverer([]))
            .with_processor(_ok)
            .build()
        )
        batch = await job.execute()
        assert batch.total == 1
        assert batch.results[0].status is StepStatus.SKIP

    @staticmethod
    @pytest.mark.asyncio
    async def test_maps_and_fans_out() -> None:
        """Test that discovered items are fanned out through the processor.

        **Why this test is important:**
          - This is the job's core contract — discover then process each item
            concurrently; if items were dropped or not run, documents would silently never
            be processed.

        **What it tests:**
          - Three discovered items all run and pass under a concurrency cap of 2.
        """
        job = (
            JobBuilder[int]("ingest")
            .with_discoverer(_discoverer([1, 2, 3]))
            .with_processor(_ok)
            .with_concurrency(2)
            .build()
        )
        batch = await job.execute()
        assert batch.total == _EXPECTED_TOTAL
        assert batch.succeeded == _EXPECTED_SUCCEEDED

    @staticmethod
    @pytest.mark.asyncio
    async def test_retry_then_succeed() -> None:
        """Test that the job's internal retry re-runs a failing item until it succeeds.

        **Why this test is important:**
          - Transient failures (network blips) must not fail a document on the first try;
            the engine-internal retry (mirroring Go retryDo) is the first line of
            resiliency.

        **What it tests:**
          - With retry=2, an item that raises once then succeeds ends PASS after exactly 2
            calls.
        """
        calls: dict[int, int] = {}

        async def flaky(item: int) -> StepResult:
            await asyncio.sleep(0)
            calls[item] = calls.get(item, 0) + 1
            if calls[item] < _EXPECTED_CALLS:
                msg = "transient"
                raise ValueError(msg)
            return StepResult(name=f"item-{item}")

        job = (
            JobBuilder[int]("ingest")
            .with_discoverer(_discoverer([1]))
            .with_processor(flaky)
            .with_retry(2)
            .build()
        )
        batch = await job.execute()
        assert batch.succeeded == 1
        assert calls[1] == _EXPECTED_CALLS

    @staticmethod
    @pytest.mark.asyncio
    async def test_warn_only_downgrades_failures() -> None:
        """Test that warn_only downgrades FAIL results to WARN (non-blocking job).

        **Why this test is important:**
          - A non-blocking/advisory job must never turn a failing item into a hard failure
            that blocks a gate; warn_only is the switch that makes the job advisory
            (mirrors Go WarnOnly).

        **What it tests:**
          - Two always-failing items produce zero FAILs and two WARNs.
        """

        async def boom(item: int) -> StepResult:
            del item
            await asyncio.sleep(0)
            msg = "nope"
            raise ValueError(msg)

        job = (
            JobBuilder[int]("ingest")
            .with_discoverer(_discoverer([1, 2]))
            .with_processor(boom)
            .warn_only()
            .build()
        )
        batch = await job.execute()
        assert batch.failed == 0
        assert batch.warned == _EXPECTED_WARNED
