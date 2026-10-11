"""Process-isolated parsing: bound a parse's memory + wall-clock, kill on breach.

Each parse runs in a dedicated child process so a crashing/OOM/runaway parser is contained to a
single document (never the pod): a timeout ``terminate()``s the child, and a per-child ``RLIMIT_AS``
caps address space (best-effort — enforced on Linux, a no-op on platforms that reject it, e.g. macOS
dev). ``IsolatedParser.parse`` maps a breach to a failed ``ParsedDocument`` rather than raising, so the
job fails one document alone.

The child is forked from a **forkserver** that preloads the heavy parser libraries (``markitdown`` +
``pymupdf``) where the platform supports it, so each parse pays a cheap fork instead of a full
interpreter spawn + a per-parse re-import of the markitdown/pymupdf dependency tree. The
forkserver is a SEPARATE process that holds those imports, so the async parent stays lazy-import-clean.
Every parse still gets its OWN fresh, killable, memory-capped child — the forkserver only
changes how cheaply that child is created, not the isolation contract. Platforms without forkserver
(e.g. Windows) fall back to ``spawn``.
"""

from __future__ import annotations

import asyncio
import contextlib
import functools
import multiprocessing as mp
import resource
import time
import traceback
from queue import Empty
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser
from techai_webutils.core.domain import ParsedDocument

if TYPE_CHECKING:
    from collections.abc import Callable
    from multiprocessing.process import BaseProcess
    from multiprocessing.queues import Queue

# _DEFAULT_MEMORY_BYTES is the per-parse address-space cap (512 MiB, sized under the 1Gi pod
# limit so several parses can run concurrently without tripping the container OOM-killer).
_DEFAULT_MEMORY_BYTES = 512 * 1024 * 1024
"""Per-parse address-space cap (512 MiB), sized under the 1Gi pod so several parses can run at once."""

# _DEFAULT_TIMEOUT_SECONDS is the per-parse wall-clock ceiling before the child is killed.
_DEFAULT_TIMEOUT_SECONDS = 60.0
"""Per-parse wall-clock ceiling (seconds) before the child parser is killed."""

# _TERMINATE_GRACE_SECONDS is how long a SIGTERM'd child is given to exit before SIGKILL.
_TERMINATE_GRACE_SECONDS = 5.0
"""Seconds a SIGTERM'd child parser is given to exit before it is SIGKILL'd."""

# _POLL_INTERVAL_SECONDS is how often the parent re-checks the result queue + child liveness
# while waiting; small enough to notice a crashed worker promptly, large enough to be cheap.
_POLL_INTERVAL_SECONDS = 0.05
"""How often (seconds) the parent re-checks the result queue and child liveness while waiting."""

# Heavy parser libraries the forkserver preloads so a forked parse child inherits them (no per-parse
# re-import). They must be importable in the forkserver — both are hard ingestion deps.
_FORKSERVER_PRELOAD = ["markitdown", "pymupdf"]
"""Heavy parser libs the forkserver preloads so a forked parse child inherits them (no re-import)."""


def _isolated_context():  # noqa: ANN202 — inferred SpawnContext|ForkServerContext keeps .Process/.Queue
    """Return the mp context for isolated parses: forkserver (preloaded) where available, else spawn.

    forkserver forks each parse child from a server process that has already imported the heavy parser
    libs, so a parse pays a cheap fork instead of a full interpreter spawn + markitdown/pymupdf
    re-import, while every parse still gets its own killable, memory-capped child. spawn is
    the fallback where forkserver is unsupported (e.g. Windows); it re-imports per child as before.
    """
    if "forkserver" in mp.get_all_start_methods():
        ctx = mp.get_context("forkserver")
        # Sets a config only; the forkserver process (separate from this parent) does the actual
        # import when it starts lazily on the first parse — so the async parent stays import-clean.
        ctx.set_forkserver_preload(_FORKSERVER_PRELOAD)
        return ctx
    return mp.get_context("spawn")


# Built once at import: the forkserver itself is started lazily on the first Process, so this is cheap.
_MP_CONTEXT = _isolated_context()
"""The multiprocessing context for isolated parses (forkserver where available, else spawn)."""


class IsolationError(Exception):
    """Raised when an isolated task's worker dies (crash / OOM-kill) without a result."""


def _apply_memory_limit(memory_bytes: int) -> None:
    """Cap the current process's address space; ignore platforms that reject RLIMIT_AS."""
    # best-effort: RLIMIT_AS is unsupported on some platforms (e.g. macOS)
    with contextlib.suppress(ValueError, OSError):
        resource.setrlimit(resource.RLIMIT_AS, (memory_bytes, memory_bytes))


def _worker[T](
    fn: Callable[..., T],
    args: tuple[object, ...],
    memory_bytes: int,
    result_queue: Queue[tuple[str, object]],
) -> None:
    """Subprocess entry: apply the memory cap, run fn, and return its result or error."""
    if memory_bytes > 0:
        _apply_memory_limit(memory_bytes)
    try:
        result_queue.put(("ok", fn(*args)))
    # Return the failure to the parent instead of crashing silently.
    except Exception as exc:
        # Carry the full traceback (not just repr) so an IsolationError in the parent is
        # diagnosable — the child's stack is otherwise invisible (mirrors fan_out's detail).
        result_queue.put(("err", f"{exc!r}\n{traceback.format_exc()}"))


def _kill(proc: BaseProcess) -> None:
    """Terminate a runaway/failed child, escalating SIGTERM -> SIGKILL if it ignores the first."""
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
) -> T:
    """Run ``fn(*args)`` in a spawned subprocess, killing it if it exceeds ``timeout_seconds``.

    Raises ``TimeoutError`` if the child overruns (after terminating it), or ``IsolationError``
    if the child dies without producing a result or the task itself raised.
    """
    ctx = _MP_CONTEXT
    result_queue: Queue[tuple[str, object]] = ctx.Queue()
    proc = ctx.Process(target=_worker, args=(fn, args, memory_bytes, result_queue))
    proc.start()

    # Drain the result queue BEFORE join()ing. A child that put() a result does not exit
    # until its feeder thread flushes the object to the pipe, and a result larger than the
    # ~64KB pipe buffer only flushes once the parent get()s it — so joining first would
    # deadlock (then spuriously time out) on any real-sized document. We poll (rather than
    # one long get) so a child that crashes WITHOUT a result is noticed within a poll
    # interval instead of blocking the whole timeout, while still bounding a genuine overrun.
    result = _await_result(proc, result_queue, timeout_seconds)

    proc.join(_TERMINATE_GRACE_SECONDS)
    if proc.is_alive():  # result delivered but the child is lingering — reap it
        _kill(proc)
    status, payload = result
    if status == "err":
        raise IsolationError(str(payload))
    return cast("T", payload)


def _await_result(
    proc: BaseProcess,
    result_queue: Queue[tuple[str, object]],
    timeout_seconds: float,
) -> tuple[str, object]:
    """Poll for the worker's ``(status, payload)`` result, killing it on overrun.

    Raises ``TimeoutError`` if the child is still running at the deadline (after killing it),
    or ``IsolationError`` if the child exits without ever producing a result.
    """
    deadline = time.monotonic() + timeout_seconds
    while True:
        try:
            return result_queue.get(timeout=_POLL_INTERVAL_SECONDS)
        except Empty:
            if proc.is_alive():
                if time.monotonic() >= deadline:
                    _kill(proc)
                    msg = f"isolated task exceeded {timeout_seconds}s"
                    raise TimeoutError(msg) from None
                continue
            # The child exited; a result it flushed just before exiting may still be in the
            # pipe, so make one final non-racing drain before concluding it died empty.
            with contextlib.suppress(Empty):
                return result_queue.get(timeout=_POLL_INTERVAL_SECONDS)
            msg = f"isolated worker died without a result (exitcode={proc.exitcode})"
            raise IsolationError(msg) from None


def _parse_document(content: bytes, declared: str, filename: str) -> ParsedDocument:
    """Module-level parse entry (picklable for the spawned worker)."""
    return MarkItDownParser().parse(content, declared=declared, filename=filename)


def _parse_document_path(path: str, declared: str, filename: str) -> ParsedDocument:
    """Module-level path parse entry (picklable for the spawned worker; reads bytes off disk)."""
    return MarkItDownParser().parse_path(path, declared=declared, filename=filename)


class IsolatedParser:
    """Async facade that parses each document in a memory/time-bounded subprocess."""

    def __init__(
        self,
        *,
        memory_bytes: int = _DEFAULT_MEMORY_BYTES,
        timeout_seconds: float = _DEFAULT_TIMEOUT_SECONDS,
    ) -> None:
        """Configure the per-parse memory cap and timeout."""
        self._memory_bytes = memory_bytes
        self._timeout_seconds = timeout_seconds

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse in a subprocess; a timeout or worker death becomes a failed ParsedDocument."""
        loop = asyncio.get_running_loop()
        call = functools.partial(
            run_isolated,
            _parse_document,
            content,
            declared,
            filename,
            timeout_seconds=self._timeout_seconds,
            memory_bytes=self._memory_bytes,
        )
        try:
            return await loop.run_in_executor(None, call)
        except TimeoutError:
            return self._failed(content, declared, filename, "parse_timeout")
        except IsolationError as exc:
            return self._failed(
                content, declared, filename, f"parse_isolation_failed: {exc}"
            )

    @staticmethod
    def _failed(
        content: bytes, declared: str, filename: str, error: str
    ) -> ParsedDocument:
        """Build a failed ParsedDocument tagged with the detected format + error reason."""
        return ParsedDocument(
            markdown_content="",
            document_format=detect_format(declared, filename, content),
            error=error,
        )

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path`` in a subprocess (bounded memory — large-doc lane).

        The path (not the bytes) is pickled to the child, so a multi-GB document never crosses the
        process boundary in memory. A timeout or worker death becomes a failed ParsedDocument.
        """
        loop = asyncio.get_running_loop()
        call = functools.partial(
            run_isolated,
            _parse_document_path,
            path,
            declared,
            filename,
            timeout_seconds=self._timeout_seconds,
            memory_bytes=self._memory_bytes,
        )
        try:
            return await loop.run_in_executor(None, call)
        except TimeoutError:
            return self._failed_path(declared, filename, "parse_timeout")
        except IsolationError as exc:
            return self._failed_path(declared, filename, f"parse_isolation_failed: {exc}")

    @staticmethod
    def _failed_path(declared: str, filename: str, error: str) -> ParsedDocument:
        """Build a failed ParsedDocument for the path lane (format from the declared type/filename)."""
        return ParsedDocument(
            markdown_content="",
            document_format=detect_format(declared, filename, b""),
            error=error,
        )
