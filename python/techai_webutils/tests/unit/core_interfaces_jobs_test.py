"""Tests for Job interfaces and PeriodicJob dataclass."""

import pytest

from techai_webutils.core.interfaces.jobs import (
    Job,
    JobEnqueuer,
    JobScheduler,
    PeriodicJob,
    Worker,
    WorkerRegistry,
)


class TestPeriodicJob:
    """Tests for the periodic job."""

    def test_construction(self) -> None:
        """Test that PeriodicJob carries its dispatch kind, cron schedule, and args intact.

        **Why this test is important:**
          - A PeriodicJob is the unit the scheduler enqueues; if kind, schedule, or args
            were dropped or transposed, the wrong worker would run, run on the wrong cadence,
            or run with missing parameters
          - These three fields are the full contract the scheduler depends on

        **What it tests:**
          - kind, schedule, and the args mapping round-trip exactly as constructed
        """
        pj = PeriodicJob(kind="cleanup", schedule="*/5 * * * *", args={"max_age": 30})
        assert pj.kind == "cleanup"
        assert pj.schedule == "*/5 * * * *"
        assert pj.args == {"max_age": 30}

    def test_defaults(self) -> None:
        """Test that a PeriodicJob created without args defaults to an empty mapping.

        **Why this test is important:**
          - A parameterless periodic job (e.g. a ping) must not require callers to pass an
            empty dict, and the default must be a fresh per-instance mapping rather than a
            shared mutable default that one job could pollute for another

        **What it tests:**
          - Omitting args yields an empty dict
        """
        pj = PeriodicJob(kind="ping", schedule="@hourly")
        assert pj.args == {}


class TestJobABCs:
    """Tests for the job ABCs."""

    def test_cannot_instantiate_job(self) -> None:
        """Test that the Job ABC cannot be instantiated without kind/args implementations.

        **Why this test is important:**
          - Job is the contract every enqueued task implements; an instantiable stub would
            let a job with no kind() reach the worker dispatch table and never be routed
          - Forces concrete jobs to declare their dispatch identity and payload

        **What it tests:**
          - Instantiating Job directly raises TypeError because kind() and args() are abstract
        """
        with pytest.raises(TypeError):
            Job()  # type: ignore[abstract]

    def test_cannot_instantiate_worker(self) -> None:
        """Test that the Worker ABC cannot be instantiated without a work() implementation.

        **Why this test is important:**
          - Worker is the contract that actually processes a job's payload; an instantiable
            stub would silently no-op every job it was registered for, losing work
          - Forces every registered worker to supply real processing logic

        **What it tests:**
          - Instantiating Worker directly raises TypeError because work() is abstract
        """
        with pytest.raises(TypeError):
            Worker()  # type: ignore[abstract]

    def test_cannot_instantiate_job_enqueuer(self) -> None:
        """Test that the JobEnqueuer ABC cannot be instantiated without its methods.

        **Why this test is important:**
          - JobEnqueuer is the producer side of the queue; an instantiable stub would accept
            enqueue() calls and drop them, so jobs would appear submitted but never run
          - Forces concrete enqueuers to implement real submission and resource cleanup

        **What it tests:**
          - Instantiating JobEnqueuer directly raises TypeError because enqueue() and
            close() are abstract
        """
        with pytest.raises(TypeError):
            JobEnqueuer()  # type: ignore[abstract]

    def test_cannot_instantiate_job_scheduler(self) -> None:
        """Test that the JobScheduler ABC cannot be instantiated without schedule().

        **Why this test is important:**
          - JobScheduler is the contract for registering periodic work; an instantiable stub
            would accept schedule() calls and never fire them, so cron-style jobs go silent
          - Forces concrete schedulers to implement real interval registration

        **What it tests:**
          - Instantiating JobScheduler directly raises TypeError because schedule() is abstract
        """
        with pytest.raises(TypeError):
            JobScheduler()  # type: ignore[abstract]

    def test_cannot_instantiate_worker_registry(self) -> None:
        """Test that the WorkerRegistry ABC cannot be instantiated without its methods.

        **Why this test is important:**
          - WorkerRegistry is the dispatch table mapping job kinds to workers; an instantiable
            stub would return None for every get(), so dequeued jobs would find no handler
          - Forces concrete registries to implement real register/get lookup

        **What it tests:**
          - Instantiating WorkerRegistry directly raises TypeError because register() and
            get() are abstract
        """
        with pytest.raises(TypeError):
            WorkerRegistry()  # type: ignore[abstract]
