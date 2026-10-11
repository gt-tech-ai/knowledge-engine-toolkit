"""Execution-engine contracts: the async batch fan-out substrate types.

Mirrors Go's ``core/types.StepResult``/``StepResults`` and
``core/interfaces.ExecutionObserver`` for the Python ``techai_webutils.execution``
engine. Foundation-tier: pure types + protocols with no dependency outside
``core`` + stdlib.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable, Sequence
from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol, runtime_checkable

__all__ = [
    "AnyJob",
    "BatchResult",
    "Discoverer",
    "ExecutionObserver",
    "Executor",
    "JobFactory",
    "JobMeta",
    "Named",
    "NopObserver",
    "StepFn",
    "StepResult",
    "StepStatus",
]


class StepStatus(StrEnum):
    """Outcome of a single fanned-out work item (mirrors Go ``StepStatus``)."""

    PASS = "PASS"  # noqa: S105 # nosec B105 - status enum value, not a secret
    """The work item completed successfully."""
    FAIL = "FAIL"
    """The work item failed; ``error``/``detail`` carry the failure diagnostics."""
    WARN = "WARN"
    """The work item completed but with a non-fatal warning."""
    SKIP = "SKIP"
    """The work item was skipped and not processed."""


@dataclass(frozen=True, slots=True)
class StepResult:
    """Outcome of one work item processed by the engine.

    Mirrors Go ``core/types.StepResult`` (which documents every field — matched here).
    """

    name: str
    """The human-readable item name shown in result tables / logs."""

    status: StepStatus = StepStatus.PASS
    """The execution outcome (PASS/FAIL/WARN/SKIP)."""

    error: str = ""
    """The human-readable failure message; empty when the step passed."""

    detail: str = ""
    """The formatted traceback (or captured output) for failure diagnostics.

    ``str`` here, where Go's field is ``[]byte`` — Python holds the already-formatted
    traceback text rather than raw captured bytes.
    """

    group: str = ""
    """The phase/category label used to group steps in result tables.

    Carried for parity with Go's StepResult; not yet set by any producer in this package.
    """

    duration: float = 0.0
    """The wall-clock time to process the item, in seconds."""

    @property
    def ok(self) -> bool:
        """Whether the step passed."""
        return self.status is StepStatus.PASS


@dataclass(frozen=True, slots=True)
class BatchResult:
    """Aggregate outcome of a fan-out over many items (mirrors Go ``StepResults``)."""

    results: Sequence[StepResult] = ()
    """The per-item outcomes, in item order; the count properties below (total/succeeded/failed/...)
    derive from this sequence."""

    @property
    def total(self) -> int:
        """The number of items in the batch."""
        return len(self.results)

    @property
    def succeeded(self) -> int:
        """The count of items that passed."""
        return sum(1 for r in self.results if r.status is StepStatus.PASS)

    @property
    def failed(self) -> int:
        """The count of items that failed."""
        return sum(1 for r in self.results if r.status is StepStatus.FAIL)

    @property
    def warned(self) -> int:
        """The count of items that completed with warnings."""
        return sum(1 for r in self.results if r.status is StepStatus.WARN)

    @property
    def skipped(self) -> int:
        """The count of items that were skipped."""
        return sum(1 for r in self.results if r.status is StepStatus.SKIP)

    @property
    def has_failures(self) -> bool:
        """Whether any item failed (mirrors Go ``StepResults.HasFailures``)."""
        return any(r.status is StepStatus.FAIL for r in self.results)

    @property
    def all_passed(self) -> bool:
        """Whether every item passed; an empty batch is vacuously passed."""
        return all(r.status is StepStatus.PASS for r in self.results)

    def failed_names(self) -> list[str]:
        """Return the names of the failed items (mirrors Go ``StepResults.FailedNames``)."""
        return [r.name for r in self.results if r.status is StepStatus.FAIL]


@dataclass(frozen=True, slots=True)
class JobMeta:
    """A job's identity for display, grouping, and observer labelling (mirrors Go ``types.JobMeta``)."""

    name: str
    """The human-readable job name shown in gate/phase output, metrics, and logs."""

    group: str = ""
    """The phase/category label bucketing the job in result tables and dashboards."""


StepFn = Callable[[object], Awaitable[StepResult]]
"""Async mapper: transforms one work item into a StepResult (isolates item failure, never raises it)."""


@runtime_checkable
class ExecutionObserver(Protocol):
    """Receives the full gate -> phase -> step execution lifecycle plus the fan-out batch boundary.

    The engine calls these serially (never concurrently): ``fan_out``/``run_job_group`` funnel
    concurrent results through one task before delivery, so implementations need no locking;
    they must not block. This is the unified Python port of Go's ``ExecutionObserver``
    (the amendment un-trimming the earlier batch-only shape): the gate emits the
    gate/phase events, the fan-out primitive emits the batch/step events, and both share
    ``on_step_complete`` as the per-item event.

    Lifecycle per gate run: ``on_gate_start`` -> (per phase: ``on_phase_start`` -> per job's
    fan-out: ``on_batch_start`` -> per item ``on_step_start``? -> ``on_step_complete`` ->
    ``on_batch_complete``) -> ``on_phase_complete`` -> ... -> ``on_gate_complete``. A bare
    fan-out (no gate) emits only the batch/step events. ``on_step_start`` is best-effort and
    may be omitted by producers (e.g. ``fan_out``) that don't know an item's name up front.
    """

    def on_gate_start(self, name: str, phases: Sequence[str]) -> None:
        """Handle the gate-start event, called once before any phase runs, with the phase names."""
        ...

    def on_phase_start(self, name: str, jobs: Sequence[str]) -> None:
        """Handle a phase-start event, called once before the phase's jobs run, with the job names."""
        ...

    def on_batch_start(self, name: str, total: int) -> None:
        """Handle a fan-out batch start, called once before any item in the batch runs."""
        ...

    def on_step_start(self, name: str, group: str) -> None:
        """Handle an item-start event (best-effort; producers may omit it if the name is unknown)."""
        ...

    def on_step_complete(self, result: StepResult) -> None:
        """Handle one item's completion, called exactly once per item for any outcome."""
        ...

    def on_batch_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Handle a fan-out batch completion, called once after all items finish (elapsed seconds)."""
        ...

    def on_phase_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Handle a phase completion, called once after all the phase's jobs finish (elapsed seconds)."""
        ...

    def on_gate_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Handle the gate completion, called once after all phases finish (elapsed seconds)."""
        ...


class NopObserver:
    """No-op ``ExecutionObserver`` and the concrete base for partial observers.

    It is both the safe default when no observation is wired and the base that concrete
    observers inherit so they implement only the events they care about — every unhandled
    gate/phase/step/batch callback falls back to a no-op here, so the engine never calls a
    missing method (AttributeError) on a partial observer.
    """

    def on_gate_start(self, name: str, phases: Sequence[str]) -> None:
        """Discard the gate-start event."""

    def on_phase_start(self, name: str, jobs: Sequence[str]) -> None:
        """Discard the phase-start event."""

    def on_batch_start(self, name: str, total: int) -> None:
        """Discard the batch-start event."""

    def on_step_start(self, name: str, group: str) -> None:
        """Discard the step-start event."""

    def on_step_complete(self, result: StepResult) -> None:
        """Discard the step-complete event."""

    def on_batch_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Discard the batch-complete event."""

    def on_phase_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Discard the phase-complete event."""

    def on_gate_complete(self, name: str, result: BatchResult, elapsed: float) -> None:
        """Discard the gate-complete event."""


@runtime_checkable
class Named(Protocol):
    """The identity half of an execution unit: its ``JobMeta`` (mirrors Go ``interfaces.Named``)."""

    def meta(self) -> JobMeta:
        """Return the unit's identity for display and observer labelling."""
        ...


@runtime_checkable
class AnyJob(Named, Protocol):
    """A composable execution unit: identity plus an async run returning a ``BatchResult``.

    The gate's job type — the Python analog of Go ``interfaces.AnyJob``. Go's runner-carrying
    ``AnyJob`` vs runner-less ``Runnable`` split (and ``job.Adapt``) collapses to this single
    form: the async engine has no ``CommandRunner`` to bridge, so there is one job shape.
    """

    async def execute(self) -> BatchResult:
        """Run the unit and return its aggregate per-item outcomes."""
        ...


@runtime_checkable
class JobFactory(Protocol):
    """A typed producer of an ``AnyJob`` (mirrors Go ``interfaces.JobFactory``).

    Lets a worker model a job as a value (parameters as fields) rather than a bare closure;
    callers invoke ``job()`` to obtain the runnable unit.
    """

    def job(self) -> AnyJob:
        """Build and return the configured job."""
        ...


class Discoverer[T](Protocol):
    """Yields the work items a Job will fan out (mirrors Go ``interfaces.Discoverer[T]``)."""

    async def discover(self) -> Sequence[T]:
        """Return the work items to process."""
        ...


class Executor(Protocol):
    """Runs a mapper over items with bounded concurrency, returning a ``BatchResult``.

    The seam between in-process (``AsyncioExecutor``) and distributed (``RayExecutor``)
    fan-out. Implementations must isolate per-item failure so one
    bad item never aborts the batch.
    """

    async def run[T](
        self,
        fn: Callable[[T], Awaitable[StepResult]],
        items: Sequence[T],
        *,
        concurrency: int,
    ) -> BatchResult:
        """Apply ``fn`` to each item with the given max concurrency."""
        ...
