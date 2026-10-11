"""Job[T]: discoverer-backed, parallelised, optionally-retried unit of work."""

from techai_webutils.execution.job.job import Job, JobBuilder

__all__ = ["Job", "JobBuilder"]
