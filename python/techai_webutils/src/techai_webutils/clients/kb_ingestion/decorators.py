"""KnowledgeBaseIngestor decorators: poll-to-terminal + retry-with-backoff.

The poll half of a start → poll ingestion run, as a decorator (the single-writer half is
``clients/lock.SingleWriterRunner``). Both wrap a ``KnowledgeBaseIngestor`` and are themselves
``KnowledgeBaseIngestor``s, so they compose with the client-proxy stack
(``LoggingProxy``/``TracingProxy``/``CircuitBreakerProxy``) at the composition root:

    RetryingIngestor(bedrock)          # transient AWS errors → exponential backoff + jitter
      → CircuitBreakerProxy(..., cb)   # trip on sustained failure
        → PollingIngestor(...)         # start (fresh) + poll_ingestion_job → poll to terminal/deadline

``PollingIngestor.start_ingestion_job`` returns the FRESH job and ``poll_ingestion_job`` polls a given
``job_id`` to terminal; a caller can persist ``running``+``job_id`` between the two (so a restarted
caller reattaches to the job instead of starting a conflicting one), all guarded by the
``SingleWriterRunner`` single-writer lock.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.kb_ingestion import (
    POLL_TIMEOUT_ERROR,
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseDocument,
    KnowledgeBaseIngestor,
    ReattachableIngestor,
)
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.resilience.async_retry import retry_transient_async

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

_DEFAULT_POLL_INTERVAL_SECONDS = 5.0
"""Seconds between ``get_ingestion_job`` polls (Bedrock ingestion is not push-based)."""
_DEFAULT_MAX_POLL_SECONDS = 1800.0
"""Maximum seconds to poll before failing (30 minutes; Bedrock ingestion can take minutes)."""


class PollingIngestor(
    DelegatingAsyncResource[KnowledgeBaseIngestor], ReattachableIngestor
):
    """Wraps a ``KnowledgeBaseIngestor``, adding ``poll_ingestion_job`` (poll an existing job to terminal).

    ``start_ingestion_job`` returns the fresh (non-terminal) job; ``poll_ingestion_job`` polls a given
    ``job_id`` via ``get_ingestion_job`` until it is terminal or the max-poll deadline is exceeded (a
    timeout yields a ``FAILED`` ``poll_timeout`` job). Splitting start from poll lets a caller persist
    ``running``+``job_id`` before the poll and later reattach to a job it recorded.
    ``clock`` and ``sleep`` are injectable for deterministic tests.
    """

    def __init__(
        self,
        inner: KnowledgeBaseIngestor,
        *,
        poll_interval_seconds: float = _DEFAULT_POLL_INTERVAL_SECONDS,
        max_poll_seconds: float = _DEFAULT_MAX_POLL_SECONDS,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
        clock: Callable[[], float] | None = None,
    ) -> None:
        """Wrap ``inner`` and configure the poll interval, deadline, and clock/sleep seams."""
        self._inner = inner
        self._poll_interval = poll_interval_seconds
        self._max_poll = max_poll_seconds
        self._sleep = sleep
        # ``sleep`` defaults eagerly, but ``clock`` defaults to None and is resolved lazily in
        # ``_poll_to_terminal``: the default clock is the running loop's timer, which only exists once a
        # loop is running (i.e. at await time), so it cannot be bound here at construction.
        self._clock = clock

    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str
    ) -> IngestionJob:
        """Start a job and return the FRESH (non-terminal) handle — the poll is a separate step.

        Delegates to the wrapped ingestor's ``start`` and returns immediately, so the caller can persist
        ``running``+``job_id`` BEFORE the up-to-30-min poll — the ordering reattaching depends on. Converge
        the job with ``poll_ingestion_job``; run the whole start→poll thunk under ``SingleWriterRunner``
        so the single-writer lock is held across both.
        """
        return await self._inner.start_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
        )

    async def poll_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Poll an already-started job (by ``job_id``) until terminal — the reattach primitive.

        Fetches the job's current state via the wrapped ingestor, then polls to a terminal state (or a
        ``FAILED``/``poll_timeout`` job when the max-poll deadline is hit) — the same loop ``start`` used
        to run, now callable for a job the caller just started OR one whose ``job_id`` it recorded
        earlier, so a second ``StartIngestionJob`` (which Bedrock rejects with ConflictException) is
        never issued.
        """
        job = await self._inner.get_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )
        return await self._poll_to_terminal(knowledge_base_id, data_source_id, job)

    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a single state fetch to the wrapped ingestor."""
        return await self._inner.get_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )

    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """Delegate the per-document listing to the wrapped ingestor (no polling involved)."""
        return await self._inner.list_documents(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
        )

    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a stop request to the wrapped ingestor (no polling in the stop path)."""
        return await self._inner.stop_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )

    async def _poll_to_terminal(
        self,
        knowledge_base_id: str,
        data_source_id: str,
        job: IngestionJob,
    ) -> IngestionJob:
        """Poll ``get_ingestion_job`` until terminal or the max poll time is exceeded."""
        clock = self._clock or asyncio.get_running_loop().time
        deadline = clock() + self._max_poll
        while not job.is_terminal:
            if clock() >= deadline:
                return IngestionJob(
                    job_id=job.job_id,
                    state=IngestionJobState.FAILED,
                    error=POLL_TIMEOUT_ERROR,
                )
            await self._sleep(self._poll_interval)
            job = await self._inner.get_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                job_id=job.job_id,
            )
        return job


class RetryingIngestor(
    DelegatingAsyncResource[KnowledgeBaseIngestor], KnowledgeBaseIngestor
):
    """Wraps a ``KnowledgeBaseIngestor``, retrying transient AWS errors with exponential backoff.

    Uses ``retry_transient_async`` (tenacity ``wait_exponential_jitter``) so a transient
    ``AppError`` (TIMEOUT/UNAVAILABLE) is retried with backoff+jitter; permanent errors propagate.

    This is the ONLY transient-retry layer the KB stack should use. Do NOT also wrap the stack in the
    ``techai_webutils`` ``RetryProxy`` — the two would compose multiplicatively (3x3 attempts) under two
    different policies. ``RetryProxy`` is immediate-retry (no backoff); this ingestor is the backoff path.
    """

    def __init__(
        self,
        inner: KnowledgeBaseIngestor,
        *,
        max_attempts: int = 3,
        base_delay: float = 0.1,
        max_delay: float = 10.0,
    ) -> None:
        """Wrap ``inner`` and build the reusable transient-retry policy (exponential backoff + jitter)."""
        self._inner = inner
        # Built once — the tenacity decorator is stateless and applies a fresh retry state per call.
        self._retry = retry_transient_async(
            max_attempts=max_attempts,
            base_delay=base_delay,
            max_delay=max_delay,
        )

    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str
    ) -> IngestionJob:
        """Start a job, retrying transient failures with backoff."""

        async def _op() -> IngestionJob:
            return await self._inner.start_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
            )

        return await self._retry(_op)()

    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Fetch a job's state, retrying transient failures with backoff."""

        async def _op() -> IngestionJob:
            return await self._inner.get_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                job_id=job_id,
            )

        return await self._retry(_op)()

    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List the data source's documents, retrying transient failures with backoff."""

        async def _op() -> list[KnowledgeBaseDocument]:
            return await self._inner.list_documents(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
            )

        return await self._retry(_op)()

    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a stop request WITHOUT the retry wrapper — the caller owns the stop backoff.

        Unlike ``start``/``get``/``list`` (which wrap ``self._retry``), stop is a plain pass-through so a
        transient stop failure surfaces to the caller (typically a supervisor that stops stuck jobs),
        whose own bounded backoff is the single source of stop-retry — nesting the two would compose
        multiplicatively.
        """
        return await self._inner.stop_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )
