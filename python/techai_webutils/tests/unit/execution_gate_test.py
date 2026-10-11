"""Tests for the gate composition layer (Gate / GateBuilder / run_gate / run_job_group)."""

from __future__ import annotations

import asyncio
from unittest.mock import MagicMock

import pytest

from techai_webutils.core.interfaces.execution import (
    BatchResult,
    ExecutionObserver,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.execution.gate import Gate, new_gate, run_gate


def _ok(name: str) -> BatchResult:
    """Return a single-item passing batch."""
    return BatchResult(results=[StepResult(name=name, status=StepStatus.PASS)])


def _fail(name: str) -> BatchResult:
    """Return a single-item failing batch."""
    return BatchResult(
        results=[StepResult(name=name, status=StepStatus.FAIL, error="boom")]
    )


class _RecordingJob:
    """An in-test AnyJob recording that it ran and returning a preset BatchResult."""

    def __init__(
        self, name: str, result: BatchResult, ran: list[str] | None = None
    ) -> None:
        self._name: str = name
        self._result: BatchResult = result
        self._ran: list[str] | None = ran

    def meta(self) -> JobMeta:
        """Identify the job."""
        return JobMeta(name=self._name, group="test")

    async def execute(self) -> BatchResult:
        """Record execution and return the preset result."""
        await asyncio.sleep(0)
        if self._ran is not None:
            self._ran.append(self._name)
        return self._result


def _recording_observer() -> tuple[MagicMock, list[tuple[str, str]]]:
    """Build a spec-bound ExecutionObserver mock that appends lifecycle events in call order.

    Returns the mock plus the ``events`` list its gate/phase side-effects populate. The gate's
    other (batch/step) callbacks stay silent no-op mocks, so ``events`` holds exactly the
    gate/phase lifecycle in the order the gate fired it — preserving the ordering assertion.
    """
    events: list[tuple[str, str]] = []
    obs = MagicMock(spec=ExecutionObserver)
    obs.on_gate_start.side_effect = lambda name, _phases: events.append((
        "gate_start",
        name,
    ))
    obs.on_phase_start.side_effect = lambda name, _jobs: events.append((
        "phase_start",
        name,
    ))
    obs.on_phase_complete.side_effect = lambda name, _result, _elapsed: events.append((
        "phase_complete",
        name,
    ))
    obs.on_gate_complete.side_effect = lambda name, _result, _elapsed: events.append((
        "gate_complete",
        name,
    ))
    return obs, events


class TestRunGate:
    """Tests for the run gate."""

    @pytest.mark.asyncio
    async def test_aggregates_results_and_fires_lifecycle(self) -> None:
        """Test that a gate runs its phases in order, aggregates results, and fires the observer.

        **Why this test is important:**
          - The gate is the run unit the worker loop drives; if it dropped a phase's results or
            skipped a lifecycle event, the worker would under-count outcomes and starve metrics.

        **What it tests:**
          - A two-phase gate returns a BatchResult covering both phases' items, and the observer
            sees gate_start -> (phase_start -> phase_complete) x2 -> gate_complete in order.
        """
        obs, events = _recording_observer()
        gate: Gate = (
            new_gate("notify")
            .serial("p1", _RecordingJob("a", _ok("a")))
            .serial("p2", _RecordingJob("b", _ok("b")))
            .with_observer(obs)
            .build()
        )
        result = await run_gate(gate)
        assert result.total == 2
        assert result.all_passed is True
        assert events == [
            ("gate_start", "notify"),
            ("phase_start", "p1"),
            ("phase_complete", "p1"),
            ("phase_start", "p2"),
            ("phase_complete", "p2"),
            ("gate_complete", "notify"),
        ]

    @pytest.mark.asyncio
    async def test_parallel_phase_runs_every_job(self) -> None:
        """Test that a parallel phase executes all of its jobs and merges their results.

        **Why this test is important:**
          - A parallel phase must fan every job out; if it silently ran only the first, half the
            batch would go unprocessed.

        **What it tests:**
          - A two-job parallel phase runs both jobs and returns a two-item aggregate.
        """
        ran: list[str] = []
        gate = (
            new_gate("g")
            .parallel(
                "p", _RecordingJob("a", _ok("a"), ran), _RecordingJob("b", _ok("b"), ran)
            )
            .build()
        )
        result = await run_gate(gate)
        assert sorted(ran) == ["a", "b"]
        assert result.total == 2

    @pytest.mark.asyncio
    async def test_serial_phase_stops_at_first_failure(self) -> None:
        """Test that a serial phase halts after the first failing job.

        **Why this test is important:**
          - Serial phases model ordered dependencies; running a later job after an upstream failure
            would act on invalid state.

        **What it tests:**
          - Given [fail, ok] serially, the ok job never runs and the aggregate reports the failure.
        """
        ran: list[str] = []
        gate = (
            new_gate("g")
            .serial(
                "p",
                _RecordingJob("bad", _fail("bad"), ran),
                _RecordingJob("next", _ok("next"), ran),
            )
            .build()
        )
        result = await run_gate(gate)
        assert ran == ["bad"]
        assert result.has_failures is True

    @pytest.mark.asyncio
    async def test_stop_on_failure_halts_subsequent_phases(self) -> None:
        """Test that stop_on_failure skips remaining phases after a phase fails.

        **Why this test is important:**
          - When a gate is configured stop-on-failure, a failed early phase must short-circuit the
            rest; otherwise later phases run against a known-bad precondition.

        **What it tests:**
          - A stop_on_failure gate whose first phase fails never runs the second phase's job.
        """
        ran: list[str] = []
        gate = (
            new_gate("g")
            .serial("p1", _RecordingJob("bad", _fail("bad"), ran))
            .serial("p2", _RecordingJob("later", _ok("later"), ran))
            .stop_on_failure()
            .build()
        )
        result = await run_gate(gate)
        assert ran == ["bad"]
        assert result.has_failures is True

    @pytest.mark.asyncio
    async def test_default_gate_continues_past_failed_phase(self) -> None:
        """Test that without stop_on_failure a failed phase does not halt later phases.

        **Why this test is important:**
          - The default (non-stop) gate must run every phase even when an earlier one fails; halting
            by default would silently skip independent later work.

        **What it tests:**
          - A two-phase gate (no stop_on_failure) whose first phase fails still runs the second, and
            the aggregate reports the failure.
        """
        ran: list[str] = []
        gate = (
            new_gate("g")
            .serial("p1", _RecordingJob("bad", _fail("bad"), ran))
            .serial("p2", _RecordingJob("later", _ok("later"), ran))
            .build()
        )
        result = await run_gate(gate)
        assert ran == ["bad", "later"]
        assert result.has_failures is True

    @pytest.mark.asyncio
    async def test_parallel_phase_runs_jobs_concurrently(self) -> None:
        """Test that a parallel phase runs its jobs truly concurrently, not one-at-a-time.

        **Why this test is important:**
          - "parallel" must mean concurrent; a serial implementation would still pass a "both ran"
            assertion, so concurrency needs a test that a serial impl cannot satisfy.

        **What it tests:**
          - Two jobs that each await a 2-party barrier both complete — only possible if they run
            concurrently (a serial impl deadlocks and the wait_for times out).
        """
        barrier = asyncio.Barrier(2)

        class _BarrierJob:
            def __init__(self, name: str) -> None:
                self._name = name

            def meta(self) -> JobMeta:
                return JobMeta(name=self._name, group="test")

            async def execute(self) -> BatchResult:
                await barrier.wait()
                return _ok(self._name)

        gate = new_gate("g").parallel("p", _BarrierJob("a"), _BarrierJob("b")).build()
        result = await asyncio.wait_for(run_gate(gate), timeout=1.0)
        assert result.total == 2

    @pytest.mark.asyncio
    async def test_job_exception_settles_siblings_and_completes_gate(self) -> None:
        """Test that a raising job propagates, its siblings still settle, and on_gate_complete fires.

        **Why this test is important:**
          - A job raising in a parallel phase must not leave siblings running detached (orphaned
            I/O), and the gate lifecycle must still balance (on_gate_complete) before the exception
            propagates — so the caller's error handling stays honest.

        **What it tests:**
          - Given a raising job beside a good one, the good job runs to completion, run_gate re-raises
            the exception, and the observer still received gate_complete.
        """
        obs, events = _recording_observer()
        ran: list[str] = []

        class _BoomJob:
            def meta(self) -> JobMeta:
                return JobMeta(name="boom", group="test")

            async def execute(self) -> BatchResult:
                await asyncio.sleep(0)
                msg = "kaboom"
                raise RuntimeError(msg)

        gate = (
            new_gate("g")
            .parallel("p", _BoomJob(), _RecordingJob("good", _ok("good"), ran))
            .with_observer(obs)
            .build()
        )
        with pytest.raises(RuntimeError, match="kaboom"):
            await run_gate(gate)
        assert ran == ["good"]
        # Both the phase and the gate lifecycle balance even though a job raised: an observer that
        # pairs on_phase_start/on_phase_complete (or the gate pair) must not leak on a raised job.
        assert ("phase_complete", "p") in events
        assert ("gate_complete", "g") in events
