"""Process-isolated parsing: bound a parse's memory + wall-clock, kill on breach.

Each parse runs in a dedicated child process so a crashing/OOM/runaway parser is contained
to a single document (never the pod): a timeout ``terminate()``s the child, and a
per-child ``RLIMIT_AS`` caps address space (best-effort — enforced on Linux, a no-op on
platforms that reject it, e.g. macOS dev). ``IsolatedParser.parse`` maps a breach to a
failed ``ParsedDocument`` rather than raising, so the job fails one document alone.

The child is forked from a **forkserver** that preloads the heavy parser libraries
(``markitdown`` + ``pymupdf``) where the platform supports it, so each parse pays a cheap
fork instead of a full interpreter spawn + a per-parse re-import of the markitdown/pymupdf
dependency tree. The forkserver is a SEPARATE process that holds those imports, so the
async parent stays lazy-import-clean. Every parse still gets its OWN fresh, killable,
memory-capped child — the forkserver only changes how cheaply that child is created, not
the isolation contract. Platforms without forkserver (e.g. Windows) fall back to
``spawn``.

The child writes the parsed document to a scratch file (JSON) and puts nothing on the
result queue, so a large document's text is not pickled through the pipe on top of the
child's own copy. The parent's blocking wait and the read-back run on the executor the
composition root injects (sized to the lane's concurrency); with none injected, the event
loop's default pool, as before. ``cost()`` declares a parse's memory (the per-parse cap)
and CPU (one child process).
"""

from __future__ import annotations

import asyncio
import contextlib
import functools
import json
import multiprocessing as mp
import os
import resource
import tempfile
import time
import traceback
from pathlib import Path
from queue import Empty
from typing import TYPE_CHECKING, ClassVar, cast

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser
from techai_webutils.core.domain import (
    DocumentFormat,
    Page,
    ParsedDocument,
    blocks_from_json,
    blocks_to_json,
)
from techai_webutils.core.interfaces.parser import ParseCost
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Callable
    from concurrent.futures import Executor
    from multiprocessing.context import ForkServerContext, SpawnContext
    from multiprocessing.process import BaseProcess
    from multiprocessing.queues import Queue

logger = get_logger(__name__)

# _DEFAULT_MEMORY_BYTES is the per-parse address-space cap (512 MiB, sized under the 1Gi
# pod limit so several parses can run concurrently without tripping the container
# OOM-killer).
_DEFAULT_MEMORY_BYTES = 512 * 1024 * 1024
"""Per-parse address-space cap (512 MiB), sized under the 1Gi pod so several parses can
run at once."""

# _DEFAULT_TIMEOUT_SECONDS is the per-parse wall-clock ceiling before the child is killed.
_DEFAULT_TIMEOUT_SECONDS = 60.0
"""Per-parse wall-clock ceiling (seconds) before the child parser is killed."""

# _TERMINATE_GRACE_SECONDS is how long a SIGTERM'd child is given to exit before SIGKILL.
_TERMINATE_GRACE_SECONDS = 5.0
"""Seconds a SIGTERM'd child parser is given to exit before it is SIGKILL'd."""

# _POLL_INTERVAL_SECONDS is how often the parent re-checks the result queue + child
# liveness while waiting; small enough to notice a crashed worker promptly, large enough
# to be cheap.
_POLL_INTERVAL_SECONDS = 0.05
"""How often (seconds) the parent re-checks the result queue and child liveness while
waiting."""

# Heavy parser libraries the forkserver preloads so a forked parse child inherits them (no
# per-parse re-import). They must be importable in the forkserver — both are hard
# ingestion deps.
_FORKSERVER_PRELOAD = ["markitdown", "pymupdf"]
"""Heavy parser libs the forkserver preloads so a forked parse child inherits them (no
re-import)."""


def _isolated_context() -> SpawnContext | ForkServerContext:
    """Return the mp context for isolated parses.

    Forkserver (preloaded) where available, else spawn.

    forkserver forks each parse child from a server process that has already imported the
    heavy parser libs, so a parse pays a cheap fork instead of a full interpreter spawn +
    markitdown/pymupdf re-import, while every parse still gets its own killable,
    memory-capped child. spawn is the fallback where forkserver is unsupported (e.g.
    Windows); it re-imports per child as before.

    Returns:
        The mp context for isolated parses: forkserver (preloaded) where available.

    """
    if "forkserver" in mp.get_all_start_methods():
        ctx = mp.get_context("forkserver")
        # Sets a config only; the forkserver process (separate from this parent) does the
        # actual import when it starts lazily on the first parse — so the async parent
        # stays import-clean.
        ctx.set_forkserver_preload(_FORKSERVER_PRELOAD)
        return ctx
    return mp.get_context("spawn")


# Built once at import: the forkserver itself is started lazily on the first Process, so
# this is cheap.
_MP_CONTEXT = _isolated_context()
"""The multiprocessing context for isolated parses (forkserver where available, else
spawn)."""

SPAWN_CONTEXT = mp.get_context("spawn")
"""A fresh-interpreter context, for parsers whose native libraries are not fork-safe
(Docling)."""


class IsolationError(Exception):
    """Raised when an isolated task's worker dies (crash / OOM-kill) without a result."""


class IsolationMemoryError(IsolationError):
    """Raised when an isolated task ran out of memory.

    A ``MemoryError`` under its address-space cap.
    """


MEMORY_EXCEEDED = "parse_isolation_failed: memory_exceeded"
"""The error a parse carries when its child ran out of memory under the cap (keeps the
``parse_isolation_failed`` prefix consumers already classify)."""


def isolation_error_tag(exc: IsolationError) -> str:
    """Return the ``ParsedDocument.error`` for an isolated parse that failed with ``exc``.

    Args:
        exc: The isolation failure.

    Returns:
        The ``ParsedDocument.error`` for an isolated parse that failed.

    """
    if isinstance(exc, IsolationMemoryError):
        return MEMORY_EXCEEDED
    return f"parse_isolation_failed: {exc}"


def _apply_memory_limit(memory_bytes: int) -> None:
    """Cap the current process's address space; ignore platforms that reject RLIMIT_AS.

    Args:
        memory_bytes: The address-space cap; 0 leaves it unset.

    """
    # best-effort: RLIMIT_AS is unsupported on some platforms (e.g. macOS)
    with contextlib.suppress(ValueError, OSError):
        resource.setrlimit(resource.RLIMIT_AS, (memory_bytes, memory_bytes))


def _worker[T](
    fn: Callable[..., T],
    args: tuple[object, ...],
    memory_bytes: int,
    result_queue: Queue[tuple[str, object]],
) -> None:
    """Subprocess entry: apply the memory cap, run fn, and return its result or error.

    Args:
        fn: The function to run.
        args: Its arguments.
        memory_bytes: The address-space cap; 0 leaves it unset.
        result_queue: Where the ``(status, payload)`` result goes.

    """
    if memory_bytes > 0:
        _apply_memory_limit(memory_bytes)
    try:
        result_queue.put(("ok", fn(*args)))
    # Over the address-space cap: report it without allocating a traceback.
    except MemoryError:
        result_queue.put(("oom", ""))
    # Return the failure to the parent instead of crashing silently.
    except Exception as exc:
        # Carry the full traceback (not just repr) so an IsolationError in the parent is
        # diagnosable — the child's stack is otherwise invisible (mirrors fan_out's
        # detail).
        logger.exception("worker failed")
        result_queue.put(("err", f"{exc!r}\n{traceback.format_exc()}"))


def _kill(proc: BaseProcess) -> None:
    """Terminate a runaway or failed child.

    It escalates SIGTERM to SIGKILL if the first is ignored.

    Args:
        proc: The child process.

    """
    proc.terminate()
    proc.join(_TERMINATE_GRACE_SECONDS)
    if proc.is_alive():
        proc.kill()
        proc.join()


def run_isolated[T](
    fn: Callable[..., T],
    *args: object,
    timeout_seconds: float,
    memory_bytes: int = 0,
    context: SpawnContext | ForkServerContext | None = None,
) -> T:
    """Run ``fn(*args)`` in a spawned subprocess.

    It is killed if it exceeds ``timeout_seconds``.

    Raises ``TimeoutError`` if the child overruns (after terminating it),
    ``IsolationMemoryError`` if the task ran out of memory, or ``IsolationError`` if the
    child dies without producing a result or the task itself raised. ``context`` picks how
    the child starts (default: the preloaded forkserver where available).

    Args:
        fn: The picklable function to run.
        *args: Its picklable arguments.
        timeout_seconds: How long the child may run.
        memory_bytes: The child's address-space cap; 0 leaves it unset.
        context: The multiprocessing context; ``None`` uses spawn.

    Returns:
        What ``fn`` returns.

    Raises:
        IsolationError: The worker crashed or exceeded its limits.
        IsolationMemoryError: Isolated task ran out of memory.

    """
    ctx = context or _MP_CONTEXT
    result_queue: Queue[tuple[str, object]] = ctx.Queue()
    proc = ctx.Process(target=_worker, args=(fn, args, memory_bytes, result_queue))
    proc.start()

    # Drain the result queue BEFORE join()ing. A child that put() a result does not exit
    # until its feeder thread flushes the object to the pipe, and a result larger than the
    # ~64KB pipe buffer only flushes once the parent get()s it — so joining first would
    # deadlock (then spuriously time out) on any real-sized document. We poll (rather than
    # one long get) so a child that crashes WITHOUT a result is noticed within a poll
    # interval instead of blocking the whole timeout, while still bounding a genuine
    # overrun.
    result = _await_result(proc, result_queue, timeout_seconds)

    proc.join(_TERMINATE_GRACE_SECONDS)
    if proc.is_alive():  # result delivered but the child is lingering — reap it
        _kill(proc)
    status, payload = result
    if status == "oom":
        msg = "isolated task ran out of memory"
        raise IsolationMemoryError(msg)
    if status == "err":
        raise IsolationError(str(payload))
    return cast("T", payload)


def _await_result(
    proc: BaseProcess,
    result_queue: Queue[tuple[str, object]],
    timeout_seconds: float,
) -> tuple[str, object]:
    """Poll for the worker's ``(status, payload)`` result, killing it on overrun.

    Raises ``TimeoutError`` if the child is still running at the deadline (after killing
    it), or ``IsolationError`` if the child exits without ever producing a result.

    Args:
        proc: The child process.
        result_queue: Where its result arrives.
        timeout_seconds: How long to wait.

    Returns:
        The status and the payload.

    Raises:
        TimeoutError: Isolated task exceeded ``timeout_seconds``s.
        IsolationError: Isolated worker died without a result
            (exitcode=``proc.exitcode``).

    """
    deadline = time.monotonic() + timeout_seconds
    while True:
        expired = time.monotonic() >= deadline
        try:
            return result_queue.get(timeout=_POLL_INTERVAL_SECONDS)
        except Empty:
            if proc.is_alive():
                if expired:
                    _kill(proc)
                    msg = f"isolated task exceeded {timeout_seconds}s"
                    raise TimeoutError(msg) from None
                continue
            # The child exited; a result it flushed just before exiting may still be in
            # the pipe, so make one final non-racing drain before concluding it died
            # empty.
            with contextlib.suppress(Empty):
                return result_queue.get(timeout=_POLL_INTERVAL_SECONDS)
            msg = f"isolated worker died without a result (exitcode={proc.exitcode})"
            raise IsolationError(msg) from None


def _parse_document(content: bytes, declared: str, filename: str) -> ParsedDocument:
    """Module-level parse entry (picklable for the spawned worker).

    Args:
        content: The document's bytes.
        declared: The declared MIME type or extension.
        filename: The document's file name.

    Returns:
        The parsed document.

    """
    return MarkItDownParser().parse(content, declared=declared, filename=filename)


def _parse_document_path(path: str, declared: str, filename: str) -> ParsedDocument:
    """Module-level path parse entry, picklable for the spawned worker.

    It reads the bytes off disk.

    Args:
        path: The document's file.
        declared: The declared MIME type or extension.
        filename: The document's file name.

    Returns:
        The parsed document.

    """
    return MarkItDownParser().parse_path(path, declared=declared, filename=filename)


def _write_scratch(document: ParsedDocument, path: str) -> None:
    """Write ``document`` to ``path`` as JSON (the child's half of the scratch hand-back).

    Args:
        document: The parsed document.
        path: The scratch file.

    """
    payload = {
        "markdown_content": document.markdown_content,
        "document_format": document.document_format.value,
        "total_pages": document.total_pages,
        "word_count": document.word_count,
        "pages": [[page.page_number, page.text] for page in document.pages],
        "error": document.error,
        "blocks": blocks_to_json(document.blocks),
    }
    with Path(path).open("w", encoding="utf-8") as handle:
        json.dump(payload, handle)


def _read_scratch(path: str) -> ParsedDocument:
    """Read back the document ``_write_scratch`` wrote.

    The parent's half of the hand-back.

    Args:
        path: The scratch file.

    Returns:
        The parsed document.

    """
    with Path(path).open(encoding="utf-8") as handle:
        payload = cast("dict[str, object]", json.load(handle))
    pages = cast("list[list[object]]", payload["pages"])
    return ParsedDocument(
        markdown_content=cast("str", payload["markdown_content"]),
        document_format=DocumentFormat(cast("str", payload["document_format"])),
        total_pages=cast("int", payload["total_pages"]),
        word_count=cast("int", payload["word_count"]),
        pages=[Page(page_number=cast("int", n), text=cast("str", s)) for n, s in pages],
        error=cast("str", payload["error"]),
        blocks=blocks_from_json(cast("dict[str, object]", payload["blocks"])),
    )


def _parse_to_scratch(
    parse: Callable[..., ParsedDocument], scratch_path: str, *args: object
) -> None:
    """Child entry: run ``parse(*args)`` and write its document out.

    It writes to ``scratch_path`` and returns nothing.

    Args:
        parse: The parse entry.
        scratch_path: Where the document is written.
        *args: The parse entry's arguments.

    """
    _write_scratch(parse(*args), scratch_path)


class IsolatedParser:
    """Async facade that parses each document in a memory/time-bounded subprocess."""

    _context: ClassVar[SpawnContext | ForkServerContext] = _MP_CONTEXT
    """How each parse child starts (a subclass may need ``SPAWN_CONTEXT``)."""

    def __init__(
        self,
        *,
        memory_bytes: int = _DEFAULT_MEMORY_BYTES,
        timeout_seconds: float = _DEFAULT_TIMEOUT_SECONDS,
        executor: Executor | None = None,
    ) -> None:
        """Configure the per-parse memory cap, the timeout and the executor.

        The parent waits on the executor.

        ``executor`` bounds how many parses run at once; the composition root sizes it to
        the lane's concurrency. ``None`` uses the event loop's default pool (the behaviour
        before it).

        Args:
            memory_bytes: Each parse's address-space cap.
            timeout_seconds: How long each parse may run.
            executor: Runs the blocking wait off the event loop; ``None`` uses the loop's
                default.

        """
        self._memory_bytes: int = memory_bytes
        self._timeout_seconds: float = timeout_seconds
        self._executor: Executor | None = executor

    @property
    def executor(self) -> Executor | None:
        """The executor parses wait on (``None``: the event loop's default pool)."""
        return self._executor

    def cost(self) -> ParseCost:
        """Return one parse's cost: the per-parse memory cap and one CPU.

        One child process per parse.

        Returns:
            One parse's cost: the per-parse memory cap and one CPU.

        """
        return ParseCost(memory_bytes=self._memory_bytes, cpu=1.0)

    def _run_to_document(
        self, parse: Callable[..., ParsedDocument], *args: object
    ) -> ParsedDocument:
        """Run ``parse(*args)`` isolated and read its document back.

        The document comes from a scratch file; the call blocks.

        A callee raises ``TimeoutError``: The child overran the timeout.

        A callee raises ``IsolationError``: The child raised or died without a result.

        Args:
            parse: The parse entry.
            *args: Its arguments.

        Returns:
            The parsed document.

        """
        fd, scratch = tempfile.mkstemp(suffix=".parsed.json")
        os.close(fd)
        try:
            run_isolated(
                _parse_to_scratch,
                parse,
                scratch,
                *args,
                timeout_seconds=self._timeout_seconds,
                memory_bytes=self._memory_bytes,
                context=self._context,
            )
            return _read_scratch(scratch)
        finally:
            Path(scratch).unlink(missing_ok=True)

    async def _isolated(
        self, parse: Callable[..., ParsedDocument], *args: object
    ) -> ParsedDocument:
        """Run ``_run_to_document`` on the parser's executor.

        Args:
            parse: The parse entry.
            *args: Its arguments.

        Returns:
            The parsed document.

        """
        loop = asyncio.get_running_loop()
        call = functools.partial(self._run_to_document, parse, *args)
        return await loop.run_in_executor(self._executor, call)

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse in a subprocess.

        A timeout or worker death becomes a failed ParsedDocument.

        Args:
            content: The file's raw bytes.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name, a type hint and kept on the result.

        Returns:
            The parsed document.

        """
        try:
            return await self._isolated(_parse_document, content, declared, filename)
        except TimeoutError:
            return self._failed(content, declared, filename, "parse_timeout")
        except IsolationError as exc:
            return self._failed(content, declared, filename, isolation_error_tag(exc))

    @staticmethod
    def _failed(
        content: bytes, declared: str, filename: str, error: str
    ) -> ParsedDocument:
        """Build a failed ParsedDocument tagged with the detected format + error reason.

        Args:
            content: The document's bytes.
            declared: The declared MIME type or extension.
            filename: The document's file name.
            error: The failure's tag.

        Returns:
            A failed ParsedDocument tagged.

        """
        return ParsedDocument(
            markdown_content="",
            document_format=detect_format(declared, filename, content),
            error=error,
        )

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path`` in a subprocess.

        Memory is bounded: the large-document lane.

        The path (not the bytes) is pickled to the child, so a multi-GB document never
        crosses the process boundary in memory. A timeout or worker death becomes a failed
        ParsedDocument.

        Args:
            path: The file's local path.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name; empty uses the path's name.

        Returns:
            A document from a file ``path`` in a subprocess.

        """
        try:
            return await self._isolated(_parse_document_path, path, declared, filename)
        except TimeoutError:
            return self._failed_path(declared, filename, "parse_timeout")
        except IsolationError as exc:
            return self._failed_path(declared, filename, isolation_error_tag(exc))

    @staticmethod
    def _failed_path(declared: str, filename: str, error: str) -> ParsedDocument:
        """Build a failed ParsedDocument for the path lane.

        The format comes from the declared type or the filename.

        Args:
            declared: The declared MIME type or extension.
            filename: The document's file name.
            error: The failure's tag.

        Returns:
            A failed ParsedDocument for the path lane.

        """
        return ParsedDocument(
            markdown_content="",
            document_format=detect_format(declared, filename, b""),
            error=error,
        )
