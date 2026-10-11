"""Postgres advisory-lock backend for the cross-pod single-writer guard.

``PostgresAdvisoryLock`` owns ONE pinned asyncpg session and maps the ``DistributedLock`` protocol
onto session-scoped ``pg_try_advisory_lock`` / ``pg_advisory_unlock``. The session is opened lazily on
first ``acquire`` (so a Postgres blip at startup cannot fail the rest of the process's work) and held
for the process's life; if the process dies the session drops and Postgres releases the lock
automatically (no TTL). The lock is a two-int ``(classid, objid)`` — the caller's ``namespace`` plus a
signed-int32-folded crc32 of the caller's ``key``.

Cross-cutting concerns (logging, tracing, retry, circuit breaking) are layered by the generic
``clients/decorators`` proxies at the composition root; this class stays focused on the Postgres
mechanics and its connection lifecycle.
"""

from __future__ import annotations

import struct
import zlib
from typing import TYPE_CHECKING, Self

import asyncpg

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import UnavailableError
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from types import TracebackType

logger = get_logger(__name__)

# asyncpg failures treated as transient — mapped to UnavailableError so the composition-root retry /
# circuit-breaker decorators engage: server-side connection errors, client interface errors
# ("connection is closed"), raw socket errors, and a command timeout (a wedged session).
_CONNECTION_ERRORS = (
    asyncpg.PostgresConnectionError,
    asyncpg.InterfaceError,
    OSError,
    TimeoutError,
)
"""asyncpg error types treated as transient (mapped to UnavailableError for retry/circuit-breaker)."""

# Connection-level server settings for the lock session (the per-instance application_name, which tags
# the session in pg_stat_activity, is added in the constructor):
#   - Server-side TCP keepalives stop a middlebox (NAT / ELB / RDS proxy) from silently reaping the
#     long, idle session between acquires — which would look like a crash and auto-release the lock.
#     Budget: probe after 60s idle (< typical middlebox timeouts), then 4 probes 15s apart, so a dead
#     peer is detected within ~120s.
_KEEPALIVE_SETTINGS = {
    "tcp_keepalives_idle": "60",
    "tcp_keepalives_interval": "15",
    "tcp_keepalives_count": "4",
}
"""Connection-level Postgres TCP keepalive settings for the lock session."""

DEFAULT_APPLICATION_NAME = "advisory-lock"
"""The ``application_name`` a lock session reports when the caller names none."""

# The advisory-lock SQL is trivial and non-blocking; a query that outlasts this means a wedged session
# (caught as a transient error → the session is discarded and the next acquire reconnects).
_COMMAND_TIMEOUT_SECONDS = 10.0
"""Per-command timeout, in seconds; a query outlasting it signals a wedged session (transient)."""


def _to_signed_int32(value: int) -> int:
    """Reinterpret a 32-bit unsigned value (e.g. a crc32) as the signed int32 advisory locks require."""
    return struct.unpack("i", struct.pack("I", value))[0]


class PostgresAdvisoryLock:
    """Session-scoped Postgres advisory ``DistributedLock`` for a keyed single writer.

    One instance per pod owns one pinned session and locks on a two-int ``(classid, objid)`` — the
    caller's ``namespace`` + a signed-int32 hash of its ``key``. The namespace keeps a key-hash
    collision with an unrelated advisory lock from falsely serializing. It is NOT reentrant:
    ``SingleWriterRunner`` acquires once, runs, and releases, so a second ``acquire`` on a still-held
    session is not expected.
    """

    def __init__(
        self,
        *,
        dsn: str,
        key: str,
        namespace: int,
        application_name: str = DEFAULT_APPLICATION_NAME,
    ) -> None:
        """Bind the connection DSN and derive the (namespace, hash(key)) advisory-lock key.

        ``namespace`` must fit a signed int32 (Postgres ``pg_try_advisory_lock(int, int)``).
        ``application_name`` tags the session in ``pg_stat_activity``; the lock's log lines carry its
        key and namespace, so a process holding several locks can tell them apart.
        """
        if not key:
            msg = "PostgresAdvisoryLock requires a non-empty key"
            raise AppValueError(msg)
        if not -(2**31) <= namespace < 2**31:
            msg = (
                f"PostgresAdvisoryLock namespace {namespace} does not fit a signed int32"
            )
            raise AppValueError(msg)
        self._dsn = dsn
        self._key = key
        self._classid = namespace
        self._objid = _to_signed_int32(zlib.crc32(key.encode()))
        self._server_settings = {
            **_KEEPALIVE_SETTINGS,
            "application_name": application_name,
        }
        self._conn: asyncpg.Connection | None = None

    async def __aenter__(self) -> Self:
        """Enter the async context; the session is opened lazily on first ``acquire``."""
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the pinned session on shutdown, releasing any held lock."""
        await self.aclose()

    async def aclose(self) -> None:
        """Close the pinned Postgres session if it was opened."""
        if self._conn is not None:
            await self._conn.close()
            self._conn = None

    def _discard_connection(self) -> None:
        """Tear down a broken session (best-effort) so any advisory lock it still holds is released.

        ``terminate()`` aborts the socket synchronously — unlike ``close()`` it needs no round-trip on a
        connection that is already broken. Dropping the session makes Postgres auto-release the lock at
        once, instead of leaving a zombie holder until the keepalive reap (which would otherwise stall
        every replica's guarded work for that window).
        """
        conn = self._conn
        self._conn = None
        if conn is not None:
            try:
                conn.terminate()
            except Exception:  # best-effort teardown must never mask the caller's error
                logger.warning(
                    "advisory-lock session terminate failed (ignored)",
                    key=self._key,
                    namespace=self._classid,
                )

    async def _ensure_connection(self) -> asyncpg.Connection:
        """Return the pinned session, opening (or reopening) it as needed.

        Lazily connects on first use, and reconnects when a previously-opened session has been closed
        (idle-reaped or crashed) so a dead socket cannot wedge the guarded work until a restart.
        """
        conn = self._conn
        if conn is None or conn.is_closed():
            conn = await asyncpg.connect(
                self._dsn,
                server_settings=self._server_settings,
                command_timeout=_COMMAND_TIMEOUT_SECONDS,
            )
            self._conn = conn
        return conn

    async def acquire(self) -> bool:
        """Try to acquire the advisory lock; True iff this session now holds it (False if held elsewhere).

        A connection-loss failure is surfaced as a transient ``UnavailableError`` (and the broken
        session dropped so the next round reconnects), letting the composition-root retry /
        circuit-breaker decorators act on it.
        """
        try:
            conn = await self._ensure_connection()
            held = await conn.fetchval(
                "SELECT pg_try_advisory_lock($1, $2)", self._classid, self._objid
            )
        except _CONNECTION_ERRORS as err:
            # Terminate the broken session: if pg_try_advisory_lock took the lock before the ack was
            # lost, dropping the socket makes Postgres release it now rather than at the keepalive reap.
            self._discard_connection()
            msg = f"advisory-lock acquire failed (broken or unreachable Postgres session): {err}"
            raise UnavailableError(msg) from err
        return held is True

    async def release(self) -> None:
        """Release the advisory lock on the same session that acquired it.

        Tolerant of a broken session: a dropped connection already released the lock server-side, so a
        release failure is logged and swallowed rather than masking the guarded operation's result.
        """
        conn = self._conn
        if conn is None:
            return
        try:
            unlocked = await conn.fetchval(
                "SELECT pg_advisory_unlock($1, $2)", self._classid, self._objid
            )
        except _CONNECTION_ERRORS as err:
            logger.warning(
                "advisory-lock release on a broken session ignored",
                error=str(err),
                key=self._key,
                namespace=self._classid,
            )
            self._discard_connection()
        else:
            if unlocked is False:
                # A false result means this session did not hold the lock — a single-writer anomaly
                # (e.g. the session was reset mid-hold). Nothing to undo here, but it must be visible.
                logger.warning(
                    "advisory-lock release found the lock not held by this session",
                    key=self._key,
                    namespace=self._classid,
                )
