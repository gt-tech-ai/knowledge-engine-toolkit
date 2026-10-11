"""Tests for the KB-sync decorators (poll-to-terminal + single-writer lock + retry-with-backoff).

The old ``KbSyncBatcher`` dissolved into composable decorators; these preserve its four behaviours
(complete / poll-until-complete / poll-timeout / lock-skip) plus the new retry-with-backoff.
"""

import asyncio
from typing import TypedDict, Unpack
from unittest.mock import MagicMock, create_autospec

import pytest

from techai_webutils.clients.kb_ingestion.decorators import (
    PollingIngestor,
    RetryingIngestor,
)
from techai_webutils.clients.kb_ingestion.mapping import job_from_payload
from techai_webutils.clients.kb_ingestion.noop import StubKnowledgeBaseIngestor
from techai_webutils.clients.lock import InMemoryLock, SingleWriterRunner
from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.kb_ingestion import (
    IngestionJob,
    IngestionJobState,
    KnowledgeBaseIngestor,
)


async def _noop_sleep(_seconds: float) -> None:
    """Skip the wait so poll tests run instantly (a no-op sleep)."""


def _ingestor_mock() -> MagicMock:
    """Build a ``create_autospec(KnowledgeBaseIngestor)`` for the poll/retry decorator tests.

    ``list_documents`` returns no documents (these decorators exercise only start/get); each
    test wires ``start_ingestion_job``/``get_ingestion_job`` return values or side effects to
    reproduce the transitioning/stuck/flaky/always-failing scenarios the old subclass fakes
    encoded, and asserts against ``await_count`` where the fakes tracked a call counter.
    """
    mock = create_autospec(KnowledgeBaseIngestor, instance=True)
    mock.list_documents.return_value = []
    return mock


def _job(state: IngestionJobState, job_id: str = "job-1") -> IngestionJob:
    """Return an ``IngestionJob`` in ``state`` (canned return for the ingestor mocks)."""
    return IngestionJob(job_id=job_id, state=state)


class _PollingOptions(TypedDict, total=False):
    """The PollingIngestor options a test may override."""

    max_poll_seconds: float
    """The poll deadline in seconds."""


def _polling(
    inner: KnowledgeBaseIngestor, **kwargs: Unpack[_PollingOptions]
) -> PollingIngestor:
    """Build a PollingIngestor with a no-op sleep + zero interval (instant poll tests)."""
    return PollingIngestor(inner, poll_interval_seconds=0.0, sleep=_noop_sleep, **kwargs)


class TestPollingIngestor:
    """Tests for the polling ingestor."""

    @pytest.mark.asyncio
    async def test_returns_terminal_start_immediately(self) -> None:
        """Test that a job which starts terminal is returned without polling.

        **Why this test is important:**
          - The local/stub path starts COMPLETE, so document status can advance to 'indexed'
            without a real KB and without a spurious poll.

        **What it tests:**
          - start_ingestion_job over the stub returns a COMPLETE job.
        """
        job = await _polling(StubKnowledgeBaseIngestor()).start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )
        assert job.state is IngestionJobState.COMPLETE

    @pytest.mark.asyncio
    async def test_start_returns_fresh_job_without_polling(self) -> None:
        """Test that start returns the fresh (non-terminal) job and does NOT poll (split contract).

        **Why this test is important:**
          - Reattaching requires the caller to see the freshly-started job_id (still IN_PROGRESS) so
            it can persist ``running``+``job_id`` BEFORE the long poll. If ``start`` still blocked to
            terminal, that persist-before-poll ordering would be impossible.

        **What it tests:**
          - ``start_ingestion_job`` returns the inner ingestor's fresh IN_PROGRESS job verbatim and
            ``get_ingestion_job`` is never called (no poll happens in ``start``).
        """
        ingestor = _ingestor_mock()
        ingestor.start_ingestion_job.return_value = _job(
            IngestionJobState.IN_PROGRESS, "fresh"
        )
        job = await _polling(ingestor).start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )
        assert job.state is IngestionJobState.IN_PROGRESS
        assert job.job_id == "fresh"
        ingestor.get_ingestion_job.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_poll_ingestion_job_polls_until_complete(self) -> None:
        """Test that poll_ingestion_job polls a seeded job_id until it reports COMPLETE.

        **Why this test is important:**
          - This is the reattach primitive: converging an ALREADY-started job (one whose job_id the
            caller recorded earlier, or the one it just started) to terminal without a second
            StartIngestionJob that Bedrock would reject with ConflictException.

        **What it tests:**
          - poll_ingestion_job over a job that becomes COMPLETE after 2 polls returns COMPLETE and
            polled at least twice (seeded from the given job_id).
        """
        ingestor = _ingestor_mock()
        ingestor.get_ingestion_job.side_effect = [
            _job(IngestionJobState.IN_PROGRESS),
            _job(IngestionJobState.COMPLETE),
        ]
        job = await _polling(ingestor).poll_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="job-1"
        )
        assert job.state is IngestionJobState.COMPLETE
        assert ingestor.get_ingestion_job.await_count >= 2

    @pytest.mark.asyncio
    async def test_poll_ingestion_job_times_out(self) -> None:
        """Test that poll_ingestion_job on a job stuck IN_PROGRESS returns a FAILED poll_timeout.

        **Why this test is important:**
          - A wedged Bedrock job must not poll forever; the timeout lets the sync loop return and the
            document stays queued for the next tick (which will reattach via the persisted job_id).

        **What it tests:**
          - With max_poll_seconds=0 and a stuck ingestor, poll_ingestion_job returns FAILED with
            'poll_timeout'.
        """
        ingestor = _ingestor_mock()
        ingestor.get_ingestion_job.return_value = _job(
            IngestionJobState.IN_PROGRESS, "stuck"
        )
        job = await _polling(ingestor, max_poll_seconds=0.0).poll_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="stuck"
        )
        assert job.state is IngestionJobState.FAILED
        assert job.error == "poll_timeout"

    @pytest.mark.asyncio
    async def test_get_ingestion_job_delegates_to_inner(self) -> None:
        """Test that get_ingestion_job passes straight through to the wrapped ingestor.

        **Why this test is important:**
          - The decorator adds poll-to-terminal only to ``start``; a caller (or the poll loop itself)
            fetching a job's state must reach the real ingestor unaltered, or state reads would lie.

        **What it tests:**
          - get_ingestion_job over the stub returns the stub's COMPLETE job for the given id.
        """
        job = await _polling(StubKnowledgeBaseIngestor()).get_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="job-42"
        )
        assert job.job_id == "job-42"
        assert job.state is IngestionJobState.COMPLETE


class TestStopIngestionJob:
    """stop_ingestion_job across the backends + decorators (the recovery call for a stuck job)."""

    @pytest.mark.asyncio
    async def test_noop_backend_records_stop_call(self) -> None:
        """Test that the stub records the stop request and returns a STOPPING job.

        **Why this test is important:**
          - The dev/local path has no real Bedrock; the stub must satisfy the stop contract so a
            caller's stop path runs offline, and record the call so a test can assert the stop happened.

        **What it tests:**
          - ``stop_ingestion_job`` returns a STOPPING job and appends ``(kb, ds, job_id)`` to ``stopped``.
        """
        stub = StubKnowledgeBaseIngestor()
        job = await stub.stop_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="j1"
        )
        assert job.state is IngestionJobState.STOPPING
        assert ("kb", "ds", "j1") in stub.stopped

    @pytest.mark.asyncio
    async def test_decorators_delegate_stop_to_inner(self) -> None:
        """Test that PollingIngestor + RetryingIngestor delegate stop to the wrapped ingestor.

        **Why this test is important:**
          - A caller stops a job through the fully-decorated ``polling`` stack; a decorator that
            dropped or mis-forwarded the call would make the stop a silent no-op.

        **What it tests:**
          - ``PollingIngestor(RetryingIngestor(inner)).stop_ingestion_job`` awaits the inner stop with
            the job_id and returns its STOPPING job.
        """
        inner = _ingestor_mock()
        inner.stop_ingestion_job.return_value = _job(IngestionJobState.STOPPING, "j")
        stack = _polling(RetryingIngestor(inner, base_delay=0.0, max_delay=0.0))
        job = await stack.stop_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="j"
        )
        assert job.state is IngestionJobState.STOPPING
        inner.stop_ingestion_job.assert_awaited_once_with(
            knowledge_base_id="kb", data_source_id="ds", job_id="j"
        )

    @pytest.mark.asyncio
    async def test_retrying_ingestor_stop_is_retry_free(self) -> None:
        """Test that RetryingIngestor does NOT retry stop_ingestion_job (the caller owns the backoff).

        **Why this test is important:**
          - A caller that stops stuck jobs wraps stop in its own bounded backoff; if RetryingIngestor
            ALSO retried stop (like start/get/list), the two would compose into double-backoff. This pins stop
            as a plain pass-through so a future "consistency" edit can't silently reintroduce that.

        **What it tests:**
          - A transient ``AppError`` from the inner stop propagates from ``RetryingIngestor.stop_ingestion_job``
            on the FIRST call (inner awaited exactly once — no retry).
        """
        inner = _ingestor_mock()
        inner.stop_ingestion_job.side_effect = AppError(ErrorCode.UNAVAILABLE, "flaky")
        ingestor = RetryingIngestor(inner, base_delay=0.0, max_delay=0.0)
        with pytest.raises(AppError):
            await ingestor.stop_ingestion_job(
                knowledge_base_id="kb", data_source_id="ds", job_id="j"
            )
        assert inner.stop_ingestion_job.await_count == 1


class TestSingleWriterRunner:
    """Tests for the single writer runner."""

    @pytest.mark.asyncio
    async def test_runs_operation_under_free_lock(self) -> None:
        """Test that the runner runs the guarded op and returns its result when the lock is free.

        **Why this test is important:**
          - The composed sync (lock → poll → stub) must reach a terminal COMPLETE end to end.

        **What it tests:**
          - run() over a PollingIngestor(stub) returns a COMPLETE job.
        """
        polling = _polling(StubKnowledgeBaseIngestor())
        runner = SingleWriterRunner(
            lambda: polling.start_ingestion_job(
                knowledge_base_id="kb", data_source_id="ds"
            ),
            InMemoryLock(),
        )
        job = await runner.run()
        assert job is not None
        assert job.state is IngestionJobState.COMPLETE

    @pytest.mark.asyncio
    async def test_skips_when_lock_held(self) -> None:
        """Test that a contended lock skips the round without running the op (single-writer guard).

        **Why this test is important:**
          - Bedrock allows one ingestion job per KB; a second worker must NOT start a concurrent
            job. Returning None (skip) without invoking the op is the guard during a rolling update.

        **What it tests:**
          - With the lock already held, run() returns None and the operation never executes.
        """
        lock = InMemoryLock()
        assert await lock.acquire() is True  # simulate another writer holding it
        ran = False

        async def _op() -> str:
            await asyncio.sleep(0)
            nonlocal ran
            ran = True
            return "started"

        result = await SingleWriterRunner(_op, lock).run()
        assert result is None
        assert ran is False


class TestRetryingIngestor:
    """Tests for the retrying ingestor."""

    @pytest.mark.asyncio
    async def test_retries_transient_then_succeeds(self) -> None:
        """Test that transient AWS failures are retried with backoff until the start succeeds.

        **Why this test is important:**
          - Bedrock calls fail transiently (throttling/UNAVAILABLE); the client must retry rather
            than fail the whole sync on a blip.

        **What it tests:**
          - A start that fails transiently twice then succeeds returns COMPLETE after 3 attempts.
        """
        inner = _ingestor_mock()
        inner.start_ingestion_job.side_effect = [
            AppError(ErrorCode.UNAVAILABLE, "flaky"),
            AppError(ErrorCode.UNAVAILABLE, "flaky"),
            _job(IngestionJobState.COMPLETE, "ok"),
        ]
        ingestor = RetryingIngestor(inner, base_delay=0.0, max_delay=0.0)
        job = await ingestor.start_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds"
        )
        assert job.state is IngestionJobState.COMPLETE
        assert inner.start_ingestion_job.await_count == 3

    @pytest.mark.asyncio
    async def test_permanent_error_is_not_retried(self) -> None:
        """Test that a permanent (non-transient) error propagates immediately without retry.

        **Why this test is important:**
          - Retrying a permanent failure (e.g. a bad KB id / INVALID_INPUT) wastes attempts and delays
            the real failure; only transient blips should be retried.

        **What it tests:**
          - A start raising a non-transient AppError raises on the first attempt (exactly one call).
        """
        inner = _ingestor_mock()
        inner.start_ingestion_job.side_effect = AppError(
            ErrorCode.INVALID_INPUT, "always fails"
        )
        ingestor = RetryingIngestor(inner, base_delay=0.0, max_delay=0.0)
        with pytest.raises(AppError):
            await ingestor.start_ingestion_job(
                knowledge_base_id="kb", data_source_id="ds"
            )
        assert inner.start_ingestion_job.await_count == 1

    @pytest.mark.asyncio
    async def test_exhausts_attempts_then_raises(self) -> None:
        """Test that a persistently-transient failure re-raises after the attempt budget is spent.

        **Why this test is important:**
          - Retry must be bounded; a permanently-flaky Bedrock must eventually surface the failure so
            the document is failed rather than the worker retrying forever.

        **What it tests:**
          - With max_attempts=3 and an always-transient start, the last AppError re-raises after 3 calls.
        """
        inner = _ingestor_mock()
        inner.start_ingestion_job.side_effect = AppError(
            ErrorCode.UNAVAILABLE, "always fails"
        )
        ingestor = RetryingIngestor(inner, max_attempts=3, base_delay=0.0, max_delay=0.0)
        with pytest.raises(AppError):
            await ingestor.start_ingestion_job(
                knowledge_base_id="kb", data_source_id="ds"
            )
        assert inner.start_ingestion_job.await_count == 3

    @pytest.mark.asyncio
    async def test_get_ingestion_job_retries_then_succeeds(self) -> None:
        """Test that a state fetch is also retried through the same backoff policy.

        **Why this test is important:**
          - Polling a job's state hits Bedrock just as ``start`` does; a transient blip on a poll must
            be retried, not fail the whole sync — so ``get`` needs the same retry wrapper as ``start``.

        **What it tests:**
          - get_ingestion_job over the stub returns a COMPLETE job through the RetryingIngestor.
        """
        ingestor = RetryingIngestor(
            StubKnowledgeBaseIngestor(), base_delay=0.0, max_delay=0.0
        )
        job = await ingestor.get_ingestion_job(
            knowledge_base_id="kb", data_source_id="ds", job_id="j"
        )
        assert job.state is IngestionJobState.COMPLETE


class TestJobFromPayload:
    """Tests for ``job_from_payload``."""

    def test_maps_status_and_flattens_failure_reasons(self) -> None:
        """Test that a Bedrock payload maps to state + a joined failure string.

        **Why this test is important:**
          - This pure mapping is the seam between the AWS wire shape and the domain ``IngestionJob``;
            a wrong state or dropped failure reason would mislabel a document's ingestion outcome.

        **What it tests:**
          - A FAILED payload with two failureReasons yields FAILED state and a '; '-joined error.
        """
        job = job_from_payload({
            "ingestionJobId": "j1",
            "status": "FAILED",
            "failureReasons": ["bad file", "quota"],
        })
        assert job.job_id == "j1"
        assert job.state is IngestionJobState.FAILED
        assert job.error == "bad file; quota"

    def test_unknown_or_missing_status_falls_back_to_failed(self) -> None:
        """Test that an unrecognised or absent status maps to FAILED (a safe terminal state).

        **Why this test is important:**
          - A future/unknown Bedrock status must not crash the poller or look non-terminal forever;
            FAILED is the safe default that lets the document be re-queued.

        **What it tests:**
          - An unknown status and a missing status both yield FAILED.
        """
        assert (
            job_from_payload({"ingestionJobId": "j", "status": "WAT"}).state
            is IngestionJobState.FAILED
        )
        assert job_from_payload({"ingestionJobId": "j"}).state is IngestionJobState.FAILED

    def test_non_list_failure_reason_is_coerced_not_dropped(self) -> None:
        """Test that a scalar failureReasons value is coerced into the error rather than discarded.

        **Why this test is important:**
          - If Bedrock ever returns a bare string (not a list), silently dropping it would hide the
            only diagnostic on a failed ingestion.

        **What it tests:**
          - A string failureReasons becomes the error verbatim; absent reasons yield ''.
        """
        assert (
            job_from_payload({
                "ingestionJobId": "j",
                "status": "FAILED",
                "failureReasons": "boom",
            }).error
            == "boom"
        )
        assert not job_from_payload({"ingestionJobId": "j", "status": "COMPLETE"}).error

    def test_stopping_payload_maps_to_a_non_terminal_job(self) -> None:
        """Test that a STOPPING payload maps to a non-terminal job so the poller keeps polling.

        **Why this test is important:**
          - STOPPING is non-terminal in Bedrock (the job still holds Bedrock's server-side ingestion
            lock). Coercing it to terminal FAILED makes the poller stop and the single-writer lock
            release while a job is still active, so the next tick's StartIngestionJob hits
            ConflictException in a loop with no recovery.

        **What it tests:**
          - A STOPPING payload yields a STOPPING state that is NOT terminal.
        """
        job = job_from_payload({"ingestionJobId": "j", "status": "STOPPING"})
        # StrEnum compare (not IngestionJobState.STOPPING) so this fails on behavior today, not an
        # AttributeError before the member exists.
        assert job.state == "STOPPING"
        assert job.is_terminal is False

    def test_stopped_payload_maps_to_a_distinct_terminal_job(self) -> None:
        """Test that a STOPPED payload maps to a distinct terminal STOPPED state (not FAILED).

        **Why this test is important:**
          - STOPPED is a real terminal Bedrock status; mislabeling it FAILED erases the distinction
            between a failed sync and a deliberately stopped one in metrics and logs.

        **What it tests:**
          - A STOPPED payload yields a STOPPED state that IS terminal.
        """
        job = job_from_payload({"ingestionJobId": "j", "status": "STOPPED"})
        assert job.state == "STOPPED"
        assert job.is_terminal is True
