"""KnowledgeBaseIngestor decorators: poll-to-terminal + retry-with-backoff.

The poll half of a start → poll ingestion run, as a decorator (the single-writer half is
``clients/lock.SingleWriterRunner``). Both wrap a ``KnowledgeBaseIngestor`` and are
themselves ``KnowledgeBaseIngestor``s, so they compose with the client tier (a decorated
wrapper over ``clients.client.ClientBase``) at the composition root:

    RetryingIngestor(bedrock)          # transient AWS errors → exponential backoff +
    jitter
      → decorate(..., "kb_ingestion")  # the client tier: span, metrics, logs, breaker, …
        → PollingIngestor(...)         # start (fresh) + poll_ingestion_job → poll to
        terminal/deadline

``PollingIngestor.start_ingestion_job`` returns the FRESH job and ``poll_ingestion_job``
polls a given ``job_id`` to terminal; a caller can persist ``running``+``job_id`` between
the two (so a restarted caller reattaches to the job instead of starting a conflicting
one), all guarded by the ``SingleWriterRunner`` single-writer lock.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.kb_ingestion import (
    POLL_TIMEOUT_ERROR,
    DocumentIngestResult,
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseDocument,
    KnowledgeBaseIngestor,
    ReattachableIngestor,
    S3Document,
)
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource
from techai_webutils.foundation.resilience.async_retry import retry_transient_async

if TYPE_CHECKING:
    from collections.abc import (
        AsyncIterator,
        Awaitable,
        Callable,
        Collection,
        Sequence,
    )

_DEFAULT_POLL_INTERVAL_SECONDS = 5.0
"""Seconds between ``get_ingestion_job`` polls (Bedrock ingestion is not push-based)."""
_DEFAULT_MAX_POLL_SECONDS = 1800.0
"""Maximum seconds to poll before failing (30 minutes; Bedrock ingestion can take
minutes)."""


@final
class PollingIngestor(
    DelegatingAsyncResource[KnowledgeBaseIngestor], ReattachableIngestor
):
    """Wraps a ``KnowledgeBaseIngestor``, adding ``poll_ingestion_job``.

    It polls an existing job until it is terminal.

    ``start_ingestion_job`` returns the fresh (non-terminal) job; ``poll_ingestion_job``
    polls a given ``job_id`` via ``get_ingestion_job`` until it is terminal or the
    max-poll deadline is exceeded (a timeout yields a ``FAILED`` ``poll_timeout`` job).
    Splitting start from poll lets a caller persist ``running``+``job_id`` before the poll
    and later reattach to a job it recorded. ``clock`` and ``sleep`` are injectable for
    deterministic tests.
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
        """Wrap ``inner`` and configure the poll interval and deadline.

        The clock and sleep are seams.

        Args:
            inner: The ingestor whose jobs are polled.
            poll_interval_seconds: The pause between polls.
            max_poll_seconds: How long to poll before giving up.
            sleep: Waits between polls.
            clock: Reads the monotonic time; ``None`` uses ``time.monotonic``.

        """
        super().__init__(inner)
        self._poll_interval = poll_interval_seconds
        self._max_poll = max_poll_seconds
        self._sleep = sleep
        # ``sleep`` defaults eagerly, but ``clock`` defaults to None and is resolved
        # lazily in ``_poll_to_terminal``: the default clock is the running loop's timer,
        # which only exists once a loop is running (i.e. at await time), so it cannot be
        # bound here at construction.
        self._clock = clock

    @override
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, round_id: str = ""
    ) -> IngestionJob:
        """Start a job and return its FRESH (non-terminal) handle.

        Polling is a separate step.

        Delegates to the wrapped ingestor's ``start`` and returns immediately, so the
        caller can persist ``running``+``job_id`` BEFORE the up-to-30-min poll — the
        ordering reattaching depends on. Converge the job with ``poll_ingestion_job``; run
        the whole start→poll thunk under ``SingleWriterRunner`` so the single-writer lock
        is held across both.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            round_id: The sync round, so a retried start attaches to the job it already
                started; empty starts a new job.

        Returns:
            A job and return the FRESH (non-terminal) handle.

        """
        return await self._inner.start_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            round_id=round_id,
        )

    @override
    async def poll_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Poll an already-started job (by ``job_id``) until terminal.

        This is the reattach primitive.

        Fetches the job's current state via the wrapped ingestor, then polls to a terminal
        state (or a ``FAILED``/``poll_timeout`` job when the max-poll deadline is hit) —
        the same loop ``start`` used to run, now callable for a job the caller just
        started OR one whose ``job_id`` it recorded earlier, so a second
        ``StartIngestionJob`` (which Bedrock rejects with ConflictException) is never
        issued.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to wait on.

        Returns:
            The terminal job.

        """
        job = await self._inner.get_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )
        return await self._poll_to_terminal(knowledge_base_id, data_source_id, job)

    @override
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a single state fetch to the wrapped ingestor.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to read.

        Returns:
            The job in its latest state.

        """
        return await self._inner.get_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )

    @override
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """Delegate the per-document listing to the wrapped ingestor.

        No polling is involved.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.

        Returns:
            The documents the wrapped ingestor lists.

        """
        return await self._inner.list_documents(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
        )

    @override
    async def iter_cohort(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        s3_uris: Collection[str],
    ) -> AsyncIterator[KnowledgeBaseDocument]:
        """Delegate the cohort lookup to the wrapped ingestor (no polling involved).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            s3_uris: The source objects the sync round started with.

        Yields:
            Each document in the cohort.

        """
        async for doc in self._inner.iter_cohort(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            s3_uris=s3_uris,
        ):
            yield doc

    @override
    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a stop request to the wrapped ingestor (no polling in the stop path).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to stop.

        Returns:
            The job after the stop request.

        """
        return await self._inner.stop_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )

    @override
    async def ingest_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        documents: Sequence[S3Document],
    ) -> list[DocumentIngestResult]:
        """Delegate a direct ingest to the wrapped ingestor (no job, so no polling).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            documents: The documents to ingest.

        Returns:
            The wrapped ingestor's per-document results.

        """
        return await self._inner.ingest_documents(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            documents=documents,
        )

    @override
    async def delete_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        uris: Sequence[str],
    ) -> list[DocumentIngestResult]:
        """Delegate a direct delete to the wrapped ingestor (no job, so no polling).

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            uris: The documents to delete.

        Returns:
            The wrapped ingestor's per-document results.

        """
        return await self._inner.delete_documents(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            uris=uris,
        )

    async def _poll_to_terminal(
        self,
        knowledge_base_id: str,
        data_source_id: str,
        job: IngestionJob,
    ) -> IngestionJob:
        """Poll ``get_ingestion_job`` until terminal or the max poll time is exceeded.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job: The started job.

        Returns:
            The terminal job, or a failed job when the poll time runs out.

        """
        clock = self._clock or asyncio.get_running_loop().time
        deadline = clock() + self._max_poll
        while not job.is_terminal:
            if clock() >= deadline:
                return IngestionJob(
                    job_id=job.job_id,
                    state=IngestionJobState.FAILED,
                    error=POLL_TIMEOUT_ERROR,
                    documents_scanned=job.documents_scanned,
                    documents_failed=job.documents_failed,
                )
            await self._sleep(self._poll_interval)
            job = await self._inner.get_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                job_id=job.job_id,
            )
        return job


@final
class RetryingIngestor(
    DelegatingAsyncResource[KnowledgeBaseIngestor], KnowledgeBaseIngestor
):
    """Wraps a ``KnowledgeBaseIngestor``, retrying transient AWS errors.

    Retries back off exponentially.

    Uses ``retry_transient_async`` (the shared ``exponential_jitter_wait``) so a transient
    ``AppError`` (TIMEOUT/UNAVAILABLE) is retried with backoff+jitter; permanent errors
    propagate.

    This is the ONLY transient-retry layer the KB stack should use. Do NOT also decorate
    the stack with a client tier whose retry layer is on — the two would compose
    multiplicatively (3x3 attempts) under two different policies.
    """

    def __init__(
        self,
        inner: KnowledgeBaseIngestor,
        *,
        max_attempts: int = 3,
        base_delay: float = 0.1,
        max_delay: float = 10.0,
    ) -> None:
        """Wrap ``inner`` and build the reusable transient-retry policy.

        It backs off exponentially, with jitter.

        Args:
            inner: The ingestor whose calls are retried.
            max_attempts: The most attempts per call.
            base_delay: The first backoff in seconds.
            max_delay: The longest backoff in seconds.

        """
        super().__init__(inner)
        # Built once — the tenacity decorator is stateless and applies a fresh retry state
        # per call.
        self._retry = retry_transient_async(
            max_attempts=max_attempts,
            base_delay=base_delay,
            max_delay=max_delay,
        )

    @override
    async def start_ingestion_job(
        self, *, knowledge_base_id: str, data_source_id: str, round_id: str = ""
    ) -> IngestionJob:
        """Start a job, retrying transient failures with backoff.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            round_id: The sync round, so a retried start attaches to the job it already
                started; empty starts a new job.

        Returns:
            A job, retrying transient failures with backoff.

        """

        async def _op() -> IngestionJob:
            return await self._inner.start_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                round_id=round_id,
            )

        return await self._retry(_op)()

    @override
    async def get_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Fetch a job's state, retrying transient failures with backoff.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to read.

        Returns:
            A job's state, retrying transient failures with backoff.

        """

        async def _op() -> IngestionJob:
            return await self._inner.get_ingestion_job(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                job_id=job_id,
            )

        return await self._retry(_op)()

    @override
    async def list_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
    ) -> list[KnowledgeBaseDocument]:
        """List the data source's documents, retrying transient failures with backoff.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.

        Returns:
            The data source's documents, retrying transient failures with backoff.

        """

        async def _op() -> list[KnowledgeBaseDocument]:
            return await self._inner.list_documents(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
            )

        return await self._retry(_op)()

    @override
    async def iter_cohort(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        s3_uris: Collection[str],
    ) -> AsyncIterator[KnowledgeBaseDocument]:
        """Delegate the cohort lookup WITHOUT the retry wrapper.

        A partly consumed iterator cannot be replayed safely, so a transient failure
        surfaces to the caller, which re-runs the lookup for the URIs it has not resolved.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            s3_uris: The source objects the sync round started with.

        Yields:
            Each document in the cohort.

        """
        async for doc in self._inner.iter_cohort(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            s3_uris=s3_uris,
        ):
            yield doc

    @override
    async def stop_ingestion_job(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        job_id: str,
    ) -> IngestionJob:
        """Delegate a stop request WITHOUT the retry wrapper.

        The caller owns the stop backoff.

        Unlike ``start``/``get``/``list`` and the direct ingest/delete (which wrap
        ``self._retry``), stop is a plain pass-through so a transient stop failure
        surfaces to the caller (typically a supervisor that stops stuck jobs), whose own
        bounded backoff is the single source of stop-retry — nesting the two would
        compose multiplicatively.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            job_id: The job to stop.

        Returns:
            The job after the stop request.

        """
        return await self._inner.stop_ingestion_job(
            knowledge_base_id=knowledge_base_id,
            data_source_id=data_source_id,
            job_id=job_id,
        )

    @override
    async def ingest_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        documents: Sequence[S3Document],
    ) -> list[DocumentIngestResult]:
        """Ingest documents directly, retrying transient failures with backoff.

        Ingesting a document again replaces it, so a retry is safe even when an earlier
        attempt landed part of the batch.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            documents: The documents to ingest.

        Returns:
            The per-document results.

        """

        async def _op() -> list[DocumentIngestResult]:
            return await self._inner.ingest_documents(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                documents=documents,
            )

        return await self._retry(_op)()

    @override
    async def delete_documents(
        self,
        *,
        knowledge_base_id: str,
        data_source_id: str,
        uris: Sequence[str],
    ) -> list[DocumentIngestResult]:
        """Delete documents directly, retrying transient failures with backoff.

        Args:
            knowledge_base_id: The knowledge base.
            data_source_id: The data source within it.
            uris: The documents to delete.

        Returns:
            The per-document results.

        """

        async def _op() -> list[DocumentIngestResult]:
            return await self._inner.delete_documents(
                knowledge_base_id=knowledge_base_id,
                data_source_id=data_source_id,
                uris=uris,
            )

        return await self._retry(_op)()
