"""Tests for the execution-engine contracts (StepResult / BatchResult / observer / job)."""

from typing import override

from techai_webutils.core.interfaces.execution import (
    AnyJob,
    BatchResult,
    ExecutionObserver,
    JobFactory,
    JobMeta,
    Named,
    NopObserver,
    StepResult,
    StepStatus,
)


class TestBatchResult:
    """Tests for the batch result."""

    def test_aggregates_mixed_statuses(self) -> None:
        """Test that BatchResult reports counts over a mixed result set.

        **Why this test is important:**
          - The fan-out engine returns a BatchResult; a caller routes DLQ/retry and
            emits metrics off these counts, so a miscount misroutes work items or corrupts metrics.

        **What it tests:**
          - total, succeeded, failed, warned, skipped, has_failures, all_passed, failed_names
            over a set containing every status.
        """
        results = [
            StepResult(name="a", status=StepStatus.PASS),
            StepResult(name="b", status=StepStatus.FAIL, error="boom"),
            StepResult(name="c", status=StepStatus.WARN),
            StepResult(name="d", status=StepStatus.SKIP),
            StepResult(name="e", status=StepStatus.PASS),
        ]
        batch = BatchResult(results=results)
        assert batch.total == 5
        assert batch.succeeded == 2
        assert batch.failed == 1
        assert batch.warned == 1
        assert batch.skipped == 1
        assert batch.has_failures is True
        assert batch.all_passed is False
        assert batch.failed_names() == ["b"]

    def test_empty_batch_is_all_passed_no_failures(self) -> None:
        """Test that an empty BatchResult is vacuously all-passed with no failures.

        **Why this test is important:**
          - An empty poll (no messages) must read as "nothing failed", not as a failure, or an
            idle consumer would emit false failure signals (mirrors Go StepResults.AllPassed).

        **What it tests:**
          - total 0, all_passed True, has_failures False on the empty aggregate.
        """
        batch = BatchResult()
        assert batch.total == 0
        assert batch.all_passed is True
        assert batch.has_failures is False

    def test_step_result_ok_reflects_pass(self) -> None:
        """Test that StepResult.ok is True only for a PASS status.

        **Why this test is important:**
          - `ok` is the per-item success predicate a caller reads to decide delete vs
            redrive; a wrong value deletes a failed message (data loss) or redrives a good one.

        **What it tests:**
          - ok is True for PASS and False for FAIL.
        """
        assert StepResult(name="a", status=StepStatus.PASS).ok is True
        assert StepResult(name="b", status=StepStatus.FAIL).ok is False


class TestJobMeta:
    """Tests for the job meta."""

    def test_carries_name_and_group(self) -> None:
        """Test that JobMeta carries a job's display name and group.

        **Why this test is important:**
          - The gate/observer label every job by its Meta; a wrong or missing name mislabels the
            step in metrics and logs, breaking per-job attribution (mirrors Go types.JobMeta).

        **What it tests:**
          - name and group round-trip; group defaults to empty.
        """
        m = JobMeta(name="dispatch", group="notification")
        assert m.name == "dispatch"
        assert m.group == "notification"
        assert not JobMeta(name="x").group


class _StubJob:
    """A minimal in-test AnyJob (contract fixture, not a fake of a real dependency)."""

    def __init__(self, result: BatchResult) -> None:
        self._result: BatchResult = result

    def meta(self) -> JobMeta:
        """Return a fixed identity for the stub."""
        return JobMeta(name="stub", group="test")

    async def execute(self) -> BatchResult:
        """Return the preset BatchResult."""
        return self._result


class TestJobProtocols:
    """Tests for the job protocols."""

    def test_stub_satisfies_named_and_anyjob(self) -> None:
        """Test that a concrete job satisfies the Named and AnyJob protocols structurally.

        **Why this test is important:**
          - The gate composes anything shaped like an AnyJob; if the runtime-checkable contract
            didn't recognise a real job, gate composition would reject valid work units.

        **What it tests:**
          - isinstance against Named and AnyJob for a class exposing meta() + async execute().
        """
        job = _StubJob(BatchResult())
        assert isinstance(job, Named)
        assert isinstance(job, AnyJob)

    def test_job_factory_protocol(self) -> None:
        """Test that a typed producer satisfies the JobFactory protocol.

        **Why this test is important:**
          - JobFactory lets a worker model a job as a value (params as fields) rather than a bare
            closure; the contract must recognise a `job()`-exposing producer.

        **What it tests:**
          - isinstance against JobFactory for a class exposing job() -> AnyJob.
        """

        class _Factory:
            def job(self) -> AnyJob:
                return _StubJob(BatchResult())

        assert isinstance(_Factory(), JobFactory)


class TestNopObserver:
    """Tests for the no-op observer."""

    def test_satisfies_protocol_and_is_noop(self) -> None:
        """Test that NopObserver conforms to the unified ExecutionObserver and every callback no-ops.

        **Why this test is important:**
          - NopObserver is the default the engine + gate use when nothing is wired; if any of the
            gate/phase/step/batch callbacks raised or broke the isinstance contract, an unobserved
            gate run would crash.

        **What it tests:**
          - isinstance(NopObserver(), ExecutionObserver) and that each lifecycle callback across the
            full gate -> phase -> step / batch surface returns None without raising.
        """
        obs = NopObserver()
        assert isinstance(obs, ExecutionObserver)
        # Batch / step lifecycle (the fan-out primitive).
        assert obs.on_batch_start("ingest", 3) is None
        assert obs.on_step_start("a", "g") is None
        assert obs.on_step_complete(StepResult(name="a")) is None
        assert obs.on_batch_complete("ingest", BatchResult(), 0.0) is None
        # Gate / phase lifecycle (the composition layer — unification).
        assert obs.on_gate_start("gate", ["phase"]) is None
        assert obs.on_phase_start("phase", ["job"]) is None
        assert obs.on_phase_complete("phase", BatchResult(), 0.0) is None
        assert obs.on_gate_complete("gate", BatchResult(), 0.0) is None

    def test_subclass_inherits_noops(self) -> None:
        """Test that a subclass overriding one callback inherits the rest as no-ops.

        **Why this test is important:**
          - Concrete observers (e.g. the metrics observer) implement only the events they care
            about; inheriting NopObserver must supply the remaining gate/phase/step callbacks so the
            engine never calls a missing method (AttributeError) on a partial observer.

        **What it tests:**
          - A NopObserver subclass overriding only on_step_complete still satisfies the protocol and
            its inherited callbacks run without raising.
        """
        seen: list[str] = []

        class Partial(NopObserver):
            @override
            def on_step_complete(self, result: StepResult) -> None:
                seen.append(result.name)

        obs = Partial()
        assert isinstance(obs, ExecutionObserver)
        obs.on_gate_start("g", [])
        obs.on_phase_start("p", [])
        obs.on_step_complete(StepResult(name="x"))
        obs.on_phase_complete("p", BatchResult(), 0.0)
        obs.on_gate_complete("g", BatchResult(), 0.0)
        assert seen == ["x"]
