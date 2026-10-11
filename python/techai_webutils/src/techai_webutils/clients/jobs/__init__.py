"""Background job infrastructure.

Provides the in-memory implementation (local development and testing) of the job interfaces
defined in ``core/interfaces/jobs.py``, selected by ``new_jobs_from_config``; a persistent backend
can be added as another kind. Mirrors Go's ``go/clients/jobs/`` package (backed by River).
"""

from techai_webutils.clients.jobs.builder import new_jobs_from_config
from techai_webutils.clients.jobs.config import JobConfig, JobKind
from techai_webutils.clients.jobs.memory import (
    InMemoryJobEnqueuer,
    InMemoryJobScheduler,
    InMemoryWorkerRegistry,
)

__all__ = [
    "InMemoryJobEnqueuer",
    "InMemoryJobScheduler",
    "InMemoryWorkerRegistry",
    "JobConfig",
    "JobKind",
    "new_jobs_from_config",
]
