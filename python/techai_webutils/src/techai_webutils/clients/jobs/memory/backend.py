"""In-memory job queue implementation for development and testing.

Provides implementations of ``JobEnqueuer``, ``JobScheduler``, and
``WorkerRegistry`` that process jobs in-memory. Matches Go's stub implementations in
``go/clients/jobs/river/``.
"""

from __future__ import annotations

import logging
from typing import final, override

from techai_webutils.core.interfaces.jobs import (
    Job,
    JobEnqueuer,
    JobScheduler,
    PeriodicJob,
    Worker,
    WorkerRegistry,
)

_logger = logging.getLogger(__name__)


class InMemoryWorkerRegistry(WorkerRegistry):
    """In-memory worker registry mapping job kinds to workers."""

    def __init__(self) -> None:
        """Start with an empty kind-to-worker mapping."""
        self._workers: dict[str, Worker] = {}

    @override
    def register(self, kind: str, worker: Worker) -> None:
        """Associate a worker with a job kind.

        Args:
            kind: The job type the worker handles.
            worker: The worker that runs jobs of ``kind``.

        """
        self._workers[kind] = worker

    @override
    def get(self, kind: str) -> Worker | None:
        """Retrieve the worker registered for the given kind.

        Args:
            kind: A job type.

        Returns:
            The worker for ``kind``, or ``None``.

        """
        return self._workers.get(kind)


@final
class InMemoryJobEnqueuer(JobEnqueuer):
    """In-memory job enqueuer that dispatches jobs immediately to registered workers.

    Jobs are processed inline (awaited) rather than queued. This is suitable
    for local development and testing but not for production.
    """

    def __init__(self, registry: WorkerRegistry) -> None:
        """Store the worker ``registry`` used for dispatch and reset the enqueued log.

        Args:
            registry: Maps each job name to its worker.

        """
        self._registry = registry
        self._enqueued: list[Job] = []

    @property
    def enqueued(self) -> list[Job]:
        """Access the list of enqueued jobs (for testing)."""
        return list(self._enqueued)

    @override
    async def enqueue(self, job: Job) -> None:
        """Submit a job and dispatch immediately if a worker is registered.

        Args:
            job: The job to run asynchronously.

        """
        self._enqueued.append(job)
        worker = self._registry.get(job.kind())
        if worker is not None:
            try:
                await worker.work(job.args())
            except Exception:
                _logger.exception("Job %s failed", job.kind())
        else:
            _logger.warning("No worker registered for job kind: %s", job.kind())

    @override
    async def close(self) -> None:
        """No-op for in-memory implementation."""


class InMemoryJobScheduler(JobScheduler):
    """In-memory scheduler that records scheduled jobs.

    Does not actually run periodic tasks — records them for inspection.
    In production, a PostgreSQL-backed scheduler (like River) would handle
    periodic execution.
    """

    def __init__(self) -> None:
        """Start with an empty list of recorded (job, interval) schedules."""
        self._scheduled: list[tuple[PeriodicJob, float]] = []

    @property
    def scheduled(self) -> list[tuple[PeriodicJob, float]]:
        """Access the list of scheduled jobs (for testing)."""
        return list(self._scheduled)

    @override
    async def schedule(self, job: PeriodicJob, interval_seconds: float) -> None:
        """Record a periodic job schedule.

        Args:
            job: The job to run repeatedly.
            interval_seconds: Seconds between runs.

        """
        self._scheduled.append((job, interval_seconds))
