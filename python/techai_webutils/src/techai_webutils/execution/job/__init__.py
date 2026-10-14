"""Jobs: ``Job[T]`` and the job tier that decorates any job.

``Job[T]`` is a discoverer-backed, parallelised, optionally-retried unit of work; the job tier
(``tier``, ``JobBase``) runs any job through the decoration layers.
"""

from techai_webutils.execution.job.base import JobBase, tier
from techai_webutils.execution.job.job import Job, JobBuilder

__all__ = ["Job", "JobBase", "JobBuilder", "tier"]
