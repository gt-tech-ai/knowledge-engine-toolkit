"""Job[T]: a discoverer-backed, parallelised, optionally-retried unit of work.

The Python analog of Go's ``job.Job[T]`` — discover work items, fan each one out through
a processor with bounded concurrency, and optionally retry on failure or downgrade
failures to warnings. Retry is internal and async (mirroring Go's ``retryDo``) so this
package never depends on ``foundation`` and stays core-only.
"""

from __future__ import annotations

import asyncio
from dataclasses import replace
from typing import TYPE_CHECKING, Self

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.execution import (
    BatchResult,
    JobMeta,
    StepResult,
    StepStatus,
)
from techai_webutils.execution.engine.fan_out import fan_out

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.execution import Discoverer, ExecutionObserver

# Processor: transforms one discovered item into a StepResult; may raise to signal a
# retriable failure (the internal retry re-runs it; a final raise is isolated by fan_out).
type Processor[T] = Callable[[T], Awaitable[StepResult]]


class Job[T]:
    """Discovers items of type T, maps each through a processor, and fans them out.

    Construct via ``JobBuilder``. ``execute`` returns a single SKIP result when discovery
    is empty (mirroring Go ``Job.Execute``), otherwise the fanned-out BatchResult with
    failures optionally downgraded to warnings.
    """

    def __init__(
        self,
        name: str,
        discoverer: Discoverer[T],
        processor: Processor[T],
        *,
        group: str = "",
        concurrency: int = 0,
        retries: int = 0,
        retry_delay: float = 0.0,
        warn_only: bool = False,
        observer: ExecutionObserver | None = None,
    ) -> None:
        """Store the job configuration; prefer ``JobBuilder`` for construction."""
        # name labels the job in results/logs and names the empty-discovery SKIP.
        self._name = name
        # group buckets the job under a phase/category label in gate output + metrics.
        self._group = group
        # discoverer yields the work items to fan out.
        self._discoverer = discoverer
        # processor maps one item to a StepResult (may raise to signal a retriable failure).
        self._processor = processor
        # concurrency caps simultaneous items (<= 0 falls back to the engine default).
        self._concurrency = concurrency
        # retries is the number of extra attempts on a raised failure (0 = no retry).
        self._retries = retries
        # retry_delay is the pause (seconds) between retry attempts.
        self._retry_delay = retry_delay
        # warn_only downgrades FAIL results to WARN so the job is advisory/non-blocking.
        self._warn_only = warn_only
        # observer receives the fan-out lifecycle events (None = unobserved).
        self._observer = observer

    def meta(self) -> JobMeta:
        """Return the job's identity so a built Job satisfies ``AnyJob`` (mirrors Go ``Job.Meta``)."""
        return JobMeta(name=self._name, group=self._group)

    async def execute(self) -> BatchResult:
        """Discover items and fan them out; empty discovery yields a single SKIP.

        The empty-discovery SKIP is a job-level signal returned directly (not fanned out),
        so it deliberately bypasses the per-item observer — a SKIP marker is not a
        completed work item and must not increment the observer's completed/inflight
        series. Only a real fan-out (>= 1 item) drives the observer.
        """
        items = list(await self._discoverer.discover())
        if not items:
            return BatchResult(
                results=(
                    StepResult(
                        name=self._name, group=self._group, status=StepStatus.SKIP
                    ),
                ),
            )

        batch = await fan_out(
            items,
            self._concurrency,
            self._processor_with_retry(),
            self._observer,
            name=self._name,
        )
        if self._warn_only:
            batch = _downgrade_failures(batch)
        return batch

    def _processor_with_retry(self) -> Processor[T]:
        """Return the processor, wrapped with an internal async retry when retries > 0.

        The retry fires only on a *raised* exception (mirroring Go's retry-on-error); a
        processor that instead *returns* ``StepResult(FAIL)`` — the value fan_out isolates
        raised errors into — is treated as a terminal outcome and is NOT retried.
        """
        if self._retries <= 0:
            return self._processor

        processor = self._processor
        retries = self._retries
        delay = self._retry_delay

        async def retried(item: T) -> StepResult:
            """Re-run processor up to ``retries`` extra times on failure (mirrors Go retryDo)."""
            attempt = 0
            while True:
                try:
                    return await processor(item)
                except asyncio.CancelledError:
                    raise  # never retry cancellation
                except Exception:
                    if attempt >= retries:
                        raise
                    attempt += 1
                    if delay > 0:
                        await asyncio.sleep(delay)

        return retried


def _downgrade_failures(batch: BatchResult) -> BatchResult:
    """Downgrade every FAIL result to WARN (mirrors Go WarnOnly)."""
    return BatchResult(
        results=tuple(
            replace(r, status=StepStatus.WARN) if r.status is StepStatus.FAIL else r
            for r in batch.results
        ),
    )


class JobBuilder[T]:
    """Fluent builder for a ``Job[T]`` (mirrors Go's ``job.Builder[T]``)."""

    def __init__(self, name: str) -> None:
        """Start a builder for a job with the given name."""
        self._name = name
        self._group = ""
        self._discoverer: Discoverer[T] | None = None
        self._processor: Processor[T] | None = None
        self._concurrency = 0
        self._retries = 0
        self._retry_delay = 0.0
        self._warn_only = False
        self._observer: ExecutionObserver | None = None

    def grouped(self, group: str) -> Self:
        """Set the job's group label (phase/category) carried in its JobMeta."""
        self._group = group
        return self

    def with_discoverer(self, discoverer: Discoverer[T]) -> Self:
        """Set the discoverer that yields work items."""
        self._discoverer = discoverer
        return self

    def with_processor(self, processor: Processor[T]) -> Self:
        """Set the async processor applied to each discovered item."""
        self._processor = processor
        return self

    def with_concurrency(self, n: int) -> Self:
        """Set the maximum number of items processed concurrently."""
        self._concurrency = n
        return self

    def with_retry(self, retries: int, delay: float = 0.0) -> Self:
        """Set the number of extra retry attempts on failure and the pause between them."""
        self._retries = retries
        self._retry_delay = delay
        return self

    def warn_only(self, *, enabled: bool = True) -> Self:
        """Mark the job non-blocking: FAIL results are downgraded to WARN."""
        self._warn_only = enabled
        return self

    def with_observer(self, observer: ExecutionObserver) -> Self:
        """Set the execution observer that receives lifecycle events."""
        self._observer = observer
        return self

    def build(self) -> Job[T]:
        """Construct the Job[T]; raises ValueError if a discoverer or processor is missing."""
        if self._discoverer is None or self._processor is None:
            msg = "JobBuilder requires both a discoverer and a processor"
            raise AppValueError(msg)
        return Job(
            self._name,
            self._discoverer,
            self._processor,
            group=self._group,
            concurrency=self._concurrency,
            retries=self._retries,
            retry_delay=self._retry_delay,
            warn_only=self._warn_only,
            observer=self._observer,
        )
