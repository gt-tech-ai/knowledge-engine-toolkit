"""Background job interfaces.

Mirrors Go's ``interfaces.Job``, ``Worker``, ``PeriodicJob``,
``JobEnqueuer``, ``JobScheduler``, and ``WorkerRegistry``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field


class Job(ABC):
    """A background job that can be enqueued for processing."""

    @abstractmethod
    def kind(self) -> str:
        """Return the job type identifier used for worker dispatch.

        Returns:
            The job type a worker is registered under.

        """
        ...

    @abstractmethod
    def args(self) -> dict[str, object]:
        """Return the job's payload as a key-value map.

        Returns:
            The job's payload.

        """
        ...


class Worker(ABC):
    """Processes background jobs of a specific kind."""

    @abstractmethod
    async def work(self, args: dict[str, object]) -> None:
        """Execute a job with the given arguments.

        Args:
            args: The job's payload.

        """
        ...


@dataclass
class PeriodicJob:
    """A job that runs on a schedule."""

    kind: str
    """The job type identifier used to dispatch the run to a registered worker."""
    schedule: str
    """The recurrence schedule (e.g. a cron expression) the scheduler runs the job on."""
    args: dict[str, object] = field(default_factory=dict)
    """The payload passed to the worker on each scheduled run."""


class JobEnqueuer(ABC):
    """Enqueues background jobs for asynchronous processing."""

    @abstractmethod
    async def enqueue(self, job: Job) -> None:
        """Submit a job for asynchronous processing.

        Args:
            job: The job to run asynchronously.

        """
        ...

    @abstractmethod
    async def close(self) -> None:
        """Release resources held by the enqueuer."""
        ...


class JobScheduler(ABC):
    """Schedules periodic background jobs."""

    @abstractmethod
    async def schedule(self, job: PeriodicJob, interval_seconds: float) -> None:
        """Add a periodic job to the schedule at the given interval.

        Args:
            job: The job to run repeatedly.
            interval_seconds: Seconds between runs.

        """
        ...


class WorkerRegistry(ABC):
    """Registers and retrieves job workers by kind."""

    @abstractmethod
    def register(self, kind: str, worker: Worker) -> None:
        """Associate a worker with a job kind.

        Args:
            kind: The job type the worker handles.
            worker: The worker that runs jobs of ``kind``.

        """
        ...

    @abstractmethod
    def get(self, kind: str) -> Worker | None:
        """Retrieve the worker registered for the given kind.

        Args:
            kind: A job type.

        Returns:
            The worker registered for ``kind``, or ``None``.

        """
        ...
