"""run_job_group: run a phase's AnyJobs (parallel or serial) and merge their BatchResults.

The Python analog of Go ``engine.RunJobGroup`` — the mechanism the gate's phase runner
uses: parallel runs every job concurrently; serial runs them in order and stops after the
first job whose result has failures. Per-item step events are emitted by each job's own
fan-out, so this helper only executes + aggregates; it fires no observer events itself.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.execution import BatchResult

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.execution import AnyJob, StepResult


def merge_batches(batches: Sequence[BatchResult]) -> BatchResult:
    """Concatenate several BatchResults' per-item results into one aggregate, in order.

    Args:
        batches: The batch results, in order.

    Returns:
        The batch result.

    """
    merged: list[StepResult] = []
    for batch in batches:
        merged.extend(batch.results)
    return BatchResult(results=merged)


async def run_job_group(jobs: Sequence[AnyJob], *, parallel: bool) -> BatchResult:
    """Run ``jobs`` and merge their per-item results into one BatchResult.

    Parallel runs every job concurrently (``asyncio.gather``, preserving order); serial
    runs them in sequence and stops after the first job whose BatchResult has failures.
    Returns the merged aggregate; an empty group yields an empty (vacuously-passed)
    BatchResult.

    Parallel waits for every job to settle, so a job that raises does not leave its
    siblings running detached (orphaned tasks whose I/O would commit after the group
    "failed"); the first failed job's exception then propagates from its task's
    ``result()``, so the caller's error handling (e.g. the consumer loop's backoff) still
    fires. Cancelling the group cancels every job.

    Args:
        jobs: The jobs to run.
        parallel: Whether to run them concurrently.

    Returns:
        The batch result.

    """
    if not jobs:
        return BatchResult()

    batches: list[BatchResult]
    if parallel:
        tasks = [asyncio.create_task(job.execute()) for job in jobs]
        try:
            _ = await asyncio.wait(tasks)
        finally:
            for task in tasks:
                _ = task.cancel()
        batches = [task.result() for task in tasks]
    else:
        batches = []
        for job in jobs:
            batch = await job.execute()
            batches.append(batch)
            if batch.has_failures:
                break

    return merge_batches(batches)
