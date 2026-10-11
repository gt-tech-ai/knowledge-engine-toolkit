"""Gate composition: Phase, Gate, GateBuilder, GateFactory, run_gate (async port of gate.go).

Composes AnyJobs into ordered parallel/serial phases with optional stop-on-failure, aggregating
per-phase BatchResults and emitting the gate/phase lifecycle to the ExecutionObserver. Per-item
step events come from each job's own fan-out; the gate owns only the gate/phase boundaries.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, Self, runtime_checkable

from techai_webutils.core.interfaces.execution import BatchResult, NopObserver
from techai_webutils.execution.engine.run import merge_batches, run_job_group

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.execution import AnyJob, ExecutionObserver


@dataclass(frozen=True, slots=True)
class Phase:
    """A named group of jobs run either in parallel or serially (mirrors Go ``gate.Phase``)."""

    name: str
    """Identifies the phase in observer events and result grouping."""

    jobs: tuple[AnyJob, ...]
    """The AnyJobs that make up this phase."""

    parallel: bool
    """Selects concurrent (True) vs sequential-stop-at-first-failure (False) execution."""


@dataclass(frozen=True, slots=True)
class Gate:
    """An ordered sequence of phases with optional stop-on-failure (mirrors Go ``gate.Gate``)."""

    name: str
    """Identifies the gate in observer events and the aggregate outcome."""

    phases: tuple[Phase, ...]
    """The ordered phases executed by run_gate."""

    observer: ExecutionObserver
    """Receives the gate/phase lifecycle events as the gate runs."""

    stop_on_failure: bool = False
    """When True, halts the remaining phases after any phase fails."""


class GateBuilder:
    """Fluent assembler for a Gate (mirrors Go ``gate.GateBuilder``)."""

    def __init__(self, name: str) -> None:
        """Start a builder for a named gate."""
        self._name: str = name
        self._phases: list[Phase] = []
        self._stop_on_failure: bool = False
        self._observer: ExecutionObserver | None = None

    def parallel(self, name: str, *jobs: AnyJob) -> Self:
        """Add a phase whose jobs all run concurrently."""
        self._phases.append(Phase(name=name, jobs=jobs, parallel=True))
        return self

    def serial(self, name: str, *jobs: AnyJob) -> Self:
        """Add a phase whose jobs run one after another, stopping at the first failure."""
        self._phases.append(Phase(name=name, jobs=jobs, parallel=False))
        return self

    def stop_on_failure(self) -> Self:
        """Configure the gate to skip remaining phases after any phase fails."""
        self._stop_on_failure = True
        return self

    def with_observer(self, observer: ExecutionObserver) -> Self:
        """Set the ExecutionObserver for the built gate (defaults to NopObserver)."""
        self._observer = observer
        return self

    def build(self) -> Gate:
        """Return the configured Gate, defaulting the observer to a NopObserver."""
        observer: ExecutionObserver = (
            self._observer if self._observer is not None else NopObserver()
        )
        return Gate(
            name=self._name,
            phases=tuple(self._phases),
            observer=observer,
            stop_on_failure=self._stop_on_failure,
        )


def new_gate(name: str) -> GateBuilder:
    """Return a GateBuilder for a named gate (mirrors Go ``gate.NewGate``)."""
    return GateBuilder(name)


@runtime_checkable
class GateFactory(Protocol):
    """A typed producer of a Gate (mirrors Go ``gate.GateFactory``).

    A worker type that composes a gate implements it so the gate is a modeled value (its
    parameters as fields) rather than a bare builder call; callers invoke ``gate()``.
    """

    def gate(self) -> Gate:
        """Build and return the composed gate."""
        ...


async def run_gate(gate: Gate) -> BatchResult:
    """Run every phase of ``gate`` in order, aggregating results and firing the lifecycle.

    Fires ``on_gate_start`` -> (per phase ``on_phase_start`` -> ``on_phase_complete``) ->
    ``on_gate_complete`` on the gate's observer, and returns the merged BatchResult over all
    phases. When ``stop_on_failure`` is set, the first phase with failures halts the rest.

    Both the gate and each phase fire their ``*_complete`` event from a ``finally`` (with the
    results gathered so far), so that even a phase whose job raises still balances *both* the phase
    and the gate lifecycle before the exception propagates — mirroring Go ``RunGate``, which always
    reports completion. An observer can therefore pair ``on_phase_start``/``on_phase_complete`` (and
    the gate pair) for per-phase timing or resource cleanup without leaking on a raised job.
    """
    loop = asyncio.get_running_loop()
    start = loop.time()
    observer = gate.observer
    observer.on_gate_start(gate.name, [phase.name for phase in gate.phases])

    phase_results: list[BatchResult] = []
    try:
        for phase in gate.phases:
            phase_start = loop.time()
            observer.on_phase_start(phase.name, [job.meta().name for job in phase.jobs])
            # phase_result stays empty if run_job_group raises; the finally still fires
            # on_phase_complete so the phase lifecycle balances (mirrors the gate's finally below).
            phase_result = BatchResult()
            try:
                phase_result = await run_job_group(phase.jobs, parallel=phase.parallel)
                phase_results.append(phase_result)
            finally:
                observer.on_phase_complete(
                    phase.name, phase_result, loop.time() - phase_start
                )
            if gate.stop_on_failure and phase_result.has_failures:
                break
    finally:
        result = merge_batches(phase_results)
        observer.on_gate_complete(gate.name, result, loop.time() - start)
    return result
