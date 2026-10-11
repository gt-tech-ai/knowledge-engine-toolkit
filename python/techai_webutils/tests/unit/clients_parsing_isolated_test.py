"""Tests for process-isolated parsing (timeout-kill, worker-error, real parse)."""

import os
import resource
import time
from pathlib import Path

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.isolated import (
    IsolatedParser,
    IsolationError,
    run_isolated,
)
from techai_webutils.core.domain import DocumentFormat

# Module-level task functions so the spawn start-method can pickle them into the child.


def _double(value: int) -> int:
    """Return twice the input (fast, well-behaved task)."""
    return value * 2


def _boom() -> None:
    """Raise inside the worker to exercise error propagation."""
    msg = "kaboom"
    raise ValueError(msg)


def _sleep_forever() -> str:
    """Overrun any short timeout so the parent must kill the worker."""
    time.sleep(30)
    return "never"


def _large_result() -> str:
    """Return a payload well over the ~64KB IPC pipe buffer (exercises get-before-join)."""
    return "x" * (256 * 1024)  # 256 KiB


def _exit_without_result() -> None:
    """Exit the worker abruptly without a queue put (simulates a segfault / OOM-kill)."""
    os._exit(0)


def _address_space_limit() -> tuple[int, int]:
    """Return the worker's own RLIMIT_AS (soft, hard) pair."""
    return resource.getrlimit(resource.RLIMIT_AS)


def _make_pdf(text: str) -> bytes:
    """Build a one-page PDF containing ``text``."""
    doc = pymupdf.open()
    doc.new_page().insert_text((72, 72), text)
    return doc.tobytes()


class TestRunIsolated:
    """Tests for ``run_isolated``."""

    def test_returns_worker_result(self) -> None:
        """Test that a well-behaved task's result is returned from the subprocess.

        **Why this test is important:**
          - Isolation must be transparent for the happy path; if results didn't round-trip, every
            parse would appear to fail.

        **What it tests:**
          - run_isolated(_double, 21) returns 42.
        """
        assert run_isolated(_double, 21, timeout_seconds=30) == 42

    def test_timeout_kills_worker_and_raises(self) -> None:
        """Test that an overrunning worker is killed and TimeoutError is raised promptly.

        **Why this test is important:**
          - A runaway parser must not hang the consumer; the timeout+kill is the hard bound that
            keeps one document from stalling ingestion.

        **What it tests:**
          - A 30s-sleeping task under a 0.4s timeout raises TimeoutError in well under the sleep.
        """
        start = time.monotonic()
        with pytest.raises(TimeoutError):
            run_isolated(_sleep_forever, timeout_seconds=0.4)
        assert time.monotonic() - start < 10.0

    def test_worker_error_becomes_isolation_error(self) -> None:
        """Test that a task exception surfaces as IsolationError (not a silent success).

        **Why this test is important:**
          - A parser crash must be distinguishable from success so the document is failed, not
            marked indexed with empty content.

        **What it tests:**
          - A raising task yields IsolationError carrying the message.
        """
        with pytest.raises(IsolationError, match="kaboom"):
            run_isolated(_boom, timeout_seconds=30)

    def test_worker_error_carries_child_traceback(self) -> None:
        """Test that a task exception reaches the parent with the child's repr and traceback.

        **Why this test is important:**
          - The child's stack is otherwise invisible to the parent; without the traceback an
            isolated parse failure cannot be diagnosed from the consumer's logs.

        **What it tests:**
          - The IsolationError message holds the exception repr and the child's traceback.
        """
        with pytest.raises(IsolationError) as exc_info:
            run_isolated(_boom, timeout_seconds=30)
        message = str(exc_info.value)
        assert "ValueError('kaboom')" in message
        assert "Traceback (most recent call last)" in message

    def test_large_result_round_trips(self) -> None:
        """Test that a result larger than the IPC pipe buffer returns intact, not as a false timeout.

        **Why this test is important:**
          - A child that put()s a >64KB result cannot exit until the parent drains the queue; a
            parent that join()s before get() would deadlock and then spuriously time out on any
            real-sized document. This pins the get-before-join contract that the fix depends on.

        **What it tests:**
          - run_isolated returning a 256 KiB payload completes well within the timeout and returns
            the full payload.
        """
        start = time.monotonic()
        result = run_isolated(_large_result, timeout_seconds=30)
        assert len(result) == 256 * 1024
        assert time.monotonic() - start < 10.0

    def test_dead_worker_without_result_raises_promptly(self) -> None:
        """Test that a worker that exits without a result surfaces IsolationError, without hanging.

        **Why this test is important:**
          - A parser that segfaults / gets OOM-killed leaves no result; the parent must detect the
            dead child and fail that one document quickly, not block the whole (up to 60s) timeout.

        **What it tests:**
          - A worker that `os._exit(0)`s (no queue put) raises IsolationError well within the
            timeout window.
        """
        start = time.monotonic()
        with pytest.raises(IsolationError, match="died without a result"):
            run_isolated(_exit_without_result, timeout_seconds=30)
        assert time.monotonic() - start < 10.0


class TestWorkerMemoryCap:
    """The worker caps its own address space before running the task."""

    def test_memory_cap_applies_in_the_worker(self) -> None:
        """Test that ``memory_bytes`` becomes the worker's RLIMIT_AS where the platform allows it.

        **Why this test is important:**
          - The RLIMIT_AS cap is what bounds a runaway parser's allocation to a single document; if
            the worker skipped it, an OOM would take the pod, not one document.
          - Platforms that reject RLIMIT_AS (macOS) must degrade to no cap, never fail the task.

        **What it tests:**
          - A task reading its own RLIMIT_AS under ``memory_bytes=cap`` sees ``(cap, cap)``, or the
            inherited limit where the platform rejects the cap; either way the task completes.
        """
        cap = 64 * 1024**3
        inherited = resource.getrlimit(resource.RLIMIT_AS)
        limits = run_isolated(_address_space_limit, timeout_seconds=30, memory_bytes=cap)
        assert limits in {(cap, cap), inherited}

    def test_zero_memory_bytes_leaves_the_limit_alone(self) -> None:
        """Test that memory_bytes=0 runs the task under the inherited RLIMIT_AS.

        **Why this test is important:**
          - Zero means "no cap"; applying a zero-byte limit would make every task fail to allocate.

        **What it tests:**
          - A task reading its own RLIMIT_AS under memory_bytes=0 sees the parent's limit.
        """
        inherited = resource.getrlimit(resource.RLIMIT_AS)
        assert run_isolated(_address_space_limit, timeout_seconds=30) == inherited


class TestIsolatedParser:
    """Tests for the isolated parser."""

    @pytest.mark.asyncio
    async def test_parses_pdf_in_subprocess(self) -> None:
        """Test that IsolatedParser parses a real document end-to-end in a subprocess.

        **Why this test is important:**
          - This proves the isolation wiring (pickling, subprocess parse, result return) works with
            the real MarkItDown/pymupdf parser, not just trivial tasks.

        **What it tests:**
          - A real PDF parses ok through the subprocess with the PDF format + content preserved.
        """
        parser = IsolatedParser(timeout_seconds=60)
        result = await parser.parse(_make_pdf("Isolated content"), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert "Isolated" in result.markdown_content

    @pytest.mark.asyncio
    async def test_parses_html_in_subprocess(self) -> None:
        """Test that IsolatedParser parses HTML bytes to Markdown in a subprocess.

        **Why this test is important:**
          - HTML takes the MarkItDown conversion path, not the PDF page path, so it needs its own
            end-to-end proof through the isolated worker.

        **What it tests:**
          - An HTML heading parses ok, as HTML, to a Markdown heading.
        """
        result = await IsolatedParser(timeout_seconds=60).parse(
            b"<html><h1>Hi</h1></html>", filename="p.html"
        )
        assert result.ok
        assert result.document_format is DocumentFormat.HTML
        assert "# Hi" in result.markdown_content

    @pytest.mark.asyncio
    async def test_parse_timeout_becomes_failed_document(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Test that a subprocess timeout maps to a failed ParsedDocument, not a raise.

        **Why this test is important:**
          - A parse overrun must fail the single document (so the batch continues), tagged with the
            detected format so the caller can route it — never propagate out of the parser.

        **What it tests:**
          - When run_isolated raises TimeoutError, parse() returns not-ok with error 'parse_timeout'
            and the format still detected.
        """

        def _timeout(*_args: object, **_kwargs: object) -> object:
            raise TimeoutError

        monkeypatch.setattr(
            "techai_webutils.clients.parsing.isolated.isolated.run_isolated", _timeout
        )
        result = await IsolatedParser(timeout_seconds=1).parse(
            b"%PDF-1.7\n", filename="x.pdf"
        )
        assert not result.ok
        assert result.error == "parse_timeout"
        assert result.document_format is DocumentFormat.PDF

    @pytest.mark.asyncio
    async def test_parse_isolation_failure_becomes_failed_document(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """Test that a worker crash (IsolationError) maps to a failed ParsedDocument carrying the reason.

        **Why this test is important:**
          - A crashed/OOM-killed worker must degrade to a per-document failure with a diagnosable
            reason, not take down the parse loop.

        **What it tests:**
          - When run_isolated raises IsolationError, parse() returns not-ok with the reason folded
            into 'parse_isolation_failed: ...'.
        """

        def _iso(*_args: object, **_kwargs: object) -> object:
            msg = "worker gone"
            raise IsolationError(msg)

        monkeypatch.setattr(
            "techai_webutils.clients.parsing.isolated.isolated.run_isolated", _iso
        )
        result = await IsolatedParser().parse(b"<html></html>", filename="x.html")
        assert not result.ok
        assert "parse_isolation_failed" in result.error
        assert "worker gone" in result.error

    @pytest.mark.asyncio
    async def test_parse_path_parses_pdf_in_subprocess(self, tmp_path: Path) -> None:
        """Test that IsolatedParser.parse_path parses a real PDF from disk in a subprocess.

        **Why this test is important:**
          - The large-document lane hands the isolated parser a PATH (not pickled bytes), so the
            whole file never crosses the process boundary in memory; this proves the path wiring
            (pickle the path, subprocess parse_path, result return) works with the real parser.

        **What it tests:**
          - A real PDF written to disk parses ok through the subprocess via parse_path, PDF format +
            content preserved.
        """
        pdf = tmp_path / "doc.pdf"
        pdf.write_bytes(_make_pdf("Isolated path content"))
        result = await IsolatedParser(timeout_seconds=60).parse_path(
            str(pdf), filename="doc.pdf"
        )
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert "Isolated" in result.markdown_content

    @pytest.mark.asyncio
    async def test_parse_path_parses_html_in_subprocess(self, tmp_path: Path) -> None:
        """Test that IsolatedParser.parse_path parses an HTML file from disk in a subprocess.

        **Why this test is important:**
          - The path lane streams non-PDF formats through MarkItDown from the open file; this
            proves that branch works through the isolated worker.

        **What it tests:**
          - An HTML file on disk parses ok, as HTML, to a Markdown heading.
        """
        page = tmp_path / "p.html"
        page.write_bytes(b"<html><h1>Hi</h1></html>")
        result = await IsolatedParser(timeout_seconds=60).parse_path(
            str(page), filename="p.html"
        )
        assert result.ok
        assert result.document_format is DocumentFormat.HTML
        assert "# Hi" in result.markdown_content

    @pytest.mark.asyncio
    async def test_parse_path_worker_error_becomes_failed_document(
        self, tmp_path: Path
    ) -> None:
        """Test that a real error raised in the worker becomes a failed ParsedDocument.

        **Why this test is important:**
          - A task that raises in the child must reach the parent as an error result and fail
            that one document, not crash the consumer or report an empty success.

        **What it tests:**
          - parse_path on a missing file fails with parse_isolation_failed carrying the
            child's FileNotFoundError.
        """
        missing = tmp_path / "missing.html"
        result = await IsolatedParser(timeout_seconds=60).parse_path(
            str(missing), filename="missing.html"
        )
        assert not result.ok
        assert "parse_isolation_failed" in result.error
        assert "FileNotFoundError" in result.error
