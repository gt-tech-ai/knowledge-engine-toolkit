"""Tests for the job tier (``JobBase``) and conditional job composition (``when``)."""

from __future__ import annotations

from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest

from techai_webutils.core.errors.errors import ConflictError
from techai_webutils.core.interfaces.execution import (
    BatchResult,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.execution.job import JobBase, tier
from techai_webutils.execution.job.conditional import when
from techai_webutils.foundation.decorate import Decorations, Deps, Layer

if TYPE_CHECKING:
    from conftest import Observed

_META = JobMeta(name="reindex", group="nightly")
"""The identity of the jobs under test."""

_OK = BatchResult(results=(StepResult(name="x"),))
"""A passing batch."""


def test_tier_order_matches_go() -> None:
    """The job tier's layers, outermost first, are Go's.

    Why this test is important:
      - Both languages must decorate a job the same way.

    What it tests:
      - recovery → logging → tracing → metrics → timeout.
    """
    assert tier().order == (
        Layer.RECOVERY,
        Layer.LOGGING,
        Layer.TRACING,
        Layer.METRICS,
        Layer.TIMEOUT,
    )


@pytest.mark.asyncio
async def test_func_runs_the_job_through_the_tier(observed: Observed) -> None:
    """A job runs as ``Execute`` under its name and keeps its identity.

    Why this test is important:
      - Every job run is one span and one metric series on the batch dashboards.

    What it tests:
      - The batch is returned unchanged in a ``reindex.Execute`` span, and ``meta`` is the
        job's.
    """
    job = JobBase(
        Decorations.fixed(Deps(observability=observed.observability)), "reindex"
    ).func(_META, AsyncMock(return_value=_OK))

    assert await job.execute() is _OK
    assert job.meta() == _META
    assert [name for name, _, _ in observed.spans] == ["reindex.execute"]


@pytest.mark.asyncio
async def test_recovery_turns_a_failed_run_into_one_fail_result() -> None:
    """With recovery a failed run is one FAIL step; without it the error propagates.

    Why this test is important:
      - A batch runner reports a failed job as a result instead of aborting every other job;
        a caller that turned recovery off handles the error itself.

    What it tests:
      - An uncoded failure becomes one FAIL step named after the job; with ``skip_recovery`` a
        coded error propagates.
    """
    failing = AsyncMock(side_effect=RuntimeError("disk full"))
    batch = await JobBase(Decorations(), "reindex").func(_META, failing).execute()

    [step] = batch.results
    assert (step.name, step.group, step.status) == ("reindex", "nightly", StepStatus.FAIL)
    assert "disk full" in (step.error or "")
    raw = JobBase(Decorations.fixed(Deps(skip_recovery=True)), "reindex").func(
        _META, AsyncMock(side_effect=ConflictError("x"))
    )
    with pytest.raises(ConflictError):
        await raw.execute()


@pytest.mark.asyncio
async def test_wrap_decorates_an_existing_job(observed: Observed) -> None:
    """``wrap`` decorates any job by its own identity.

    Why this test is important:
      - Jobs built elsewhere (``Job[T]``) must be observed like ones built here.

    What it tests:
      - The wrapped job returns the inner batch in a span named after the inner job.
    """
    inner = JobBase(Decorations(), "reindex").func(_META, AsyncMock(return_value=_OK))
    job = JobBase(
        Decorations.fixed(Deps(observability=observed.observability)), "reindex"
    ).wrap(inner)

    assert await job.execute() is _OK
    assert observed.spans[0][0] == "reindex.execute"


@pytest.mark.asyncio
async def test_when_runs_or_skips_the_job() -> None:
    """``when`` runs the job only when its condition holds, else reports one SKIP.

    Why this test is important:
      - A disabled job must show as skipped, not silently vanish or run anyway.

    What it tests:
      - A false condition never runs the inner job and returns one SKIP step; a true one runs
        it and returns its batch.
    """
    run = AsyncMock(return_value=_OK)
    job = JobBase(Decorations(), "reindex").func(_META, run)

    skipped = await when(job, lambda: False).execute()
    assert [s.status for s in skipped.results] == [StepStatus.SKIP]
    run.assert_not_awaited()
    assert await when(job, lambda: True).execute() is _OK
    assert when(job, lambda: True).meta() == _META
