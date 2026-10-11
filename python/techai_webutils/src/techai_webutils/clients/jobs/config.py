"""Job queue configuration.

Mirrors Go's ``go/clients/jobs/jobs.go`` configuration types.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class JobKind(StrEnum):
    """Supported job queue backends."""

    MEMORY = "memory"
    """The in-process in-memory job queue (dev/test; no external datastore)."""


@dataclass
class JobConfig:
    """Job queue configuration."""

    kind: JobKind = JobKind.MEMORY
    """Selects the job queue backend (defaults to the in-memory queue)."""
    database_url: str = ""
    """Datastore connection URL for a persistent backend (unused by the memory kind)."""
    max_workers: int = 10
    """Maximum number of concurrent worker coroutines draining the queue."""
