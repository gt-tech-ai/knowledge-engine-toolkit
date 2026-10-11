"""Tests for background job implementation."""

from __future__ import annotations

from typing import override

import pytest

from techai_webutils.clients.jobs.config import JobConfig, JobKind
from techai_webutils.clients.jobs.memory import (
    InMemoryJobEnqueuer,
    InMemoryJobScheduler,
    InMemoryWorkerRegistry,
)
from techai_webutils.core.interfaces.jobs import Job, PeriodicJob, Worker


class SimpleJob(Job):
    """Test job for assertions."""

    def __init__(self, kind: str = "test", **kwargs: object) -> None:
        """Store the job ``kind`` and its keyword ``args``."""
        self._kind = kind
        self._args = dict(kwargs)

    @override
    def kind(self) -> str:
        return self._kind

    @override
    def args(self) -> dict[str, object]:
        return self._args


class CountingWorker(Worker):
    """Worker that counts invocations."""

    def __init__(self) -> None:
        """Start with no calls recorded."""
        self.call_count = 0
        self.last_args: dict[str, object] = {}

    @override
    async def work(self, args: dict[str, object]) -> None:
        self.call_count += 1
        self.last_args = args


class FailingWorker(Worker):
    """Worker that always raises."""

    @override
    async def work(self, args: dict[str, object]) -> None:
        msg = "worker failed"
        raise RuntimeError(msg)


class TestInMemoryWorkerRegistry:
    """Test suite for in-memory worker registry."""

    def test_register_and_get(self) -> None:
        """Test that a worker registered under a kind is retrievable by that kind.

        **Why this test is important:**
          - The registry is the dispatch table that routes each job to its handler by kind
          - If registration didn't take, the enqueuer would find no worker and the job would never run
          - Confirms the core register-then-lookup contract the queue depends on

        **What it tests:**
          - get("test") returns the exact worker instance registered under "test"
        """
        registry = InMemoryWorkerRegistry()
        worker = CountingWorker()
        registry.register("test", worker)
        assert registry.get("test") is worker

    def test_get_missing_returns_none(self) -> None:
        """Test that looking up an unregistered kind returns None rather than raising.

        **Why this test is important:**
          - Jobs may arrive for kinds with no handler; the registry must signal "none" cleanly
          - A KeyError here would crash the enqueuer instead of letting it skip/log the orphan job
          - Confirms the safe miss behavior the enqueuer relies on

        **What it tests:**
          - get("nonexistent") returns None
        """
        registry = InMemoryWorkerRegistry()
        assert registry.get("nonexistent") is None


class TestInMemoryJobEnqueuer:
    """Test suite for in-memory job enqueuer."""

    @pytest.mark.asyncio
    async def test_enqueue_dispatches_to_worker(self) -> None:
        """Test that enqueuing a job invokes its registered worker with the job's args.

        **Why this test is important:**
          - The in-memory enqueuer runs jobs inline, so enqueue must actually execute the worker
          - If dispatch or arg-passing broke, jobs would appear accepted but never do their work
          - Confirms the end-to-end path from enqueue to the correct handler with the correct payload

        **What it tests:**
          - The matching worker is invoked exactly once
          - The worker receives the job's args ({"data": "hello"})
        """
        registry = InMemoryWorkerRegistry()
        worker = CountingWorker()
        registry.register("test", worker)

        enqueuer = InMemoryJobEnqueuer(registry)
        await enqueuer.enqueue(SimpleJob("test", data="hello"))

        assert worker.call_count == 1
        assert worker.last_args == {"data": "hello"}

    @pytest.mark.asyncio
    async def test_enqueue_records_job(self) -> None:
        """Test that every enqueued job is recorded for later inspection.

        **Why this test is important:**
          - The recorded list is what tests and dev tooling use to assert a job was submitted
          - Losing the record would make submission unobservable and hide dropped jobs
          - Confirms the enqueuer tracks submissions independently of whether a worker exists

        **What it tests:**
          - After enqueue, exactly one job is recorded
          - The recorded entry is the same job object that was submitted
        """
        registry = InMemoryWorkerRegistry()
        enqueuer = InMemoryJobEnqueuer(registry)

        job = SimpleJob("test")
        await enqueuer.enqueue(job)

        assert len(enqueuer.enqueued) == 1
        assert enqueuer.enqueued[0] is job

    @pytest.mark.asyncio
    async def test_enqueue_without_worker(self) -> None:
        """Test that enqueuing a job with no registered worker is recorded and does not raise.

        **Why this test is important:**
          - A job for an unknown kind must not crash the producer or block the queue
          - The job should still be recorded (and logged as unhandled) rather than silently vanish
          - Confirms the enqueuer fails soft on orphan jobs instead of propagating an error

        **What it tests:**
          - enqueue of an unregistered kind completes without raising
          - The orphan job is still recorded in the enqueued list
        """
        registry = InMemoryWorkerRegistry()
        enqueuer = InMemoryJobEnqueuer(registry)
        await enqueuer.enqueue(SimpleJob("unknown"))
        assert len(enqueuer.enqueued) == 1

    @pytest.mark.asyncio
    async def test_enqueue_failing_worker(self) -> None:
        """Test that a worker raising during processing does not propagate out of enqueue.

        **Why this test is important:**
          - A single failing job must not break the producer that enqueued it
          - The failure is expected to be caught and logged so one bad job can't take down the caller
          - Confirms the isolation boundary around worker execution

        **What it tests:**
          - enqueue completes without raising even though the worker raises
          - The job is still recorded in the enqueued list
        """
        registry = InMemoryWorkerRegistry()
        registry.register("fail", FailingWorker())

        enqueuer = InMemoryJobEnqueuer(registry)
        await enqueuer.enqueue(SimpleJob("fail"))
        assert len(enqueuer.enqueued) == 1

    @pytest.mark.asyncio
    async def test_close_is_noop(self) -> None:
        """Test that closing the in-memory enqueuer is a safe no-op.

        **Why this test is important:**
          - Callers close the enqueuer on shutdown regardless of backend; the in-memory one holds no resources
          - close() must honor the JobEnqueuer contract without raising so shutdown stays uniform across backends
          - Confirms the lifecycle method is callable and harmless

        **What it tests:**
          - close() completes without raising
        """
        registry = InMemoryWorkerRegistry()
        enqueuer = InMemoryJobEnqueuer(registry)
        await enqueuer.close()


class TestInMemoryJobScheduler:
    """Test suite for in-memory job scheduler."""

    @pytest.mark.asyncio
    async def test_schedule_records_job(self) -> None:
        """Test that scheduling a periodic job records the job and its interval.

        **Why this test is important:**
          - The in-memory scheduler doesn't run tasks; its value is recording what was registered
          - Tests and tooling assert scheduling happened by inspecting this record
          - Confirms both the job identity and the interval are preserved, since either being wrong
            would mean a periodic task runs at the wrong cadence (or not at all) in a real backend

        **What it tests:**
          - Exactly one schedule entry is recorded
          - The recorded job kind is "cleanup" and the interval is 300.0 seconds
        """
        scheduler = InMemoryJobScheduler()
        job = PeriodicJob(kind="cleanup", schedule="*/5 * * * *")

        await scheduler.schedule(job, 300.0)

        assert len(scheduler.scheduled) == 1
        recorded_job, interval = scheduler.scheduled[0]
        assert recorded_job.kind == "cleanup"
        assert interval == pytest.approx(300.0)


class TestJobConfig:
    """Test suite for job configuration."""

    def test_defaults(self) -> None:
        """Test that JobConfig defaults to the in-memory backend with sane settings.

        **Why this test is important:**
          - Code that constructs JobConfig() without args relies on these defaults to pick a backend
          - A drifted default (wrong kind, zero workers) would silently change job execution behavior
          - Pins the default contract so any change to it is deliberate

        **What it tests:**
          - kind defaults to JobKind.MEMORY
          - database_url defaults to the empty string and max_workers defaults to 10
        """
        cfg = JobConfig()
        assert cfg.kind == JobKind.MEMORY
        assert not cfg.database_url
        assert cfg.max_workers == 10

    def test_custom(self) -> None:
        """Test that explicit JobConfig fields override the defaults.

        **Why this test is important:**
          - Production deployments supply a real database_url and worker count via JobConfig
          - If overrides were ignored, the queue would run on defaults instead of the configured backend
          - Confirms operator-supplied configuration is actually retained

        **What it tests:**
          - The supplied database_url ("postgres://...") is stored on the config
        """
        cfg = JobConfig(kind=JobKind.MEMORY, database_url="postgres://...", max_workers=5)
        assert cfg.database_url == "postgres://..."
