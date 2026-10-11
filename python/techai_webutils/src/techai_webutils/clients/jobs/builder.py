"""Jobs client builder — ``new_jobs_from_config``.

The tier-root factory: selects a job-queue backend by ``JobKind`` and returns the
``JobEnqueuer`` interface, mirroring Go ``jobs.NewFromConfig``. The in-memory
backend lives in the ``memory/`` subpackage; a River/Postgres backend can be added
as another kind.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.jobs.config import JobKind
from techai_webutils.clients.jobs.memory import (
    InMemoryJobEnqueuer,
    InMemoryWorkerRegistry,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.jobs.config import JobConfig
    from techai_webutils.core.interfaces.jobs import JobEnqueuer


def new_jobs_from_config(config: JobConfig) -> JobEnqueuer:
    """Build the job enqueuer from config, selecting the backend by ``kind``."""
    if config.kind is JobKind.MEMORY:
        return InMemoryJobEnqueuer(InMemoryWorkerRegistry())
    msg = f"unknown jobs kind: {config.kind}"
    raise AppValueError(msg)
