"""run_job_group: run a phase's AnyJobs (parallel or serial) and merge their BatchResults.

The Python analog of Go ``engine.RunJobGroup`` — the mechanism the gate's phase runner uses:
parallel runs every job concurrently; serial runs them in order and stops after the first job
whose result has failures. Per-item step events are emitted by each job's own fan-out, so this
helper only executes + aggregates; it fires no observer events itself.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.execution import BatchResult

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.execution import AnyJob, StepResult


def merge_batches(batches: Sequence[BatchResult]) -> BatchResult:
    """Concatenate several BatchResults' per-item results into one aggregate, in order."""
    merged: list[StepResult] = []
    for batch in batches:
        merged.extend(batch.results)
    return BatchResult(results=merged)


async def run_job_group(jobs: Sequence[AnyJob], *, parallel: bool) -> BatchResult:
    """Run ``jobs`` and merge their per-item results into one BatchResult.

    Parallel runs every job concurrently (``asyncio.gather``, preserving order); serial runs them
    in sequence and stops after the first job whose BatchResult has failures. Returns the merged
    aggregate; an empty group yields an empty (vacuously-passed) BatchResult.

    Parallel uses ``return_exceptions=True`` so a job that raises does not leave its siblings
    running detached (orphaned tasks whose I/O would commit after the group "failed"); once every
    job has settled, the first exception is re-raised so the caller's error handling (e.g. the
    consumer loop's backoff) still fires.
    """
    if not jobs:
        return BatchResult()

    batches: list[BatchResult]
    if parallel:
        settled = await asyncio.gather(
            *(job.execute() for job in jobs), return_exceptions=True
        )
        batches = []
        for outcome in settled:
            if isinstance(outcome, BaseException):
                raise outcome
            batches.append(outcome)
    else:
        batches = []
        for job in jobs:
            batch = await job.execute()
            batches.append(batch)
            if batch.has_failures:
                break

    return merge_batches(batches)
