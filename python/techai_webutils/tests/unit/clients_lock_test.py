"""Unit tests for the lock client package (factory + backends).

Covers the ``clients/lock/`` package: ``InMemoryLock`` as an async context manager, the
``new_lock_from_config`` factory selection, and ``PostgresAdvisoryLock``
acquire/release/key/resilience against a mocked asyncpg connection. Integration (real
Postgres) lives in ``tests/integration/``.
"""

from __future__ import annotations

import struct
import zlib
from typing import cast
from unittest.mock import AsyncMock, MagicMock, patch

import asyncpg
import pytest

from techai_webutils.clients.lock import (
    InMemoryLock,
    LockConfig,
    LockKind,
    new_lock_from_config,
    postgres_dsn,
)
from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.errors.errors import UnavailableError

_FAKE_CRED_1 = "p@ss/word"

_EXPECTED_CLASSID = 1262637913
_EXPECTED_AWAIT_COUNT = 2
_EXPECTED_FIELDS = 7

INT32_MIN = -(2**31)
INT32_MAX = 2**31 - 1


def _mock_conn(*, lock_result: bool = True) -> AsyncMock:
    """Build a mocked asyncpg connection: ``fetchval`` returns ``lock_result``.

    Close/terminate/is_closed stubbed.

    Returns:
        A mocked asyncpg connection: ``fetchval`` returns ``lock_result``.

    """
    conn = AsyncMock()
    conn.fetchval = AsyncMock(return_value=lock_result)
    conn.close = AsyncMock()
    conn.terminate = MagicMock()  # asyncpg terminate() is synchronous (aborts the socket)
    conn.is_closed = MagicMock(return_value=False)
    return conn


class TestInMemoryLock:
    """Tests for the in-memory lock."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_is_async_context_manager_yielding_self() -> None:
        """Test that InMemoryLock works as an async context manager and yields itself.

        **Why this test is important:**
          - A composition root wires the lock via ``AsyncExitStack.enter_async_context``,
            which binds the lock to ``__aenter__``'s return value; a literal no-op
            returning ``None`` would hand ``SingleWriterRunner`` a ``None`` lock and crash
            on the first run.

        **What it tests:**
          - ``async with InMemoryLock()`` yields the same instance, and acquire/release
            still work.
        """
        lock = InMemoryLock()
        async with lock as entered:
            assert entered is lock
            assert await entered.acquire() is True
            await entered.release()


class TestPostgresAdvisoryLock:
    """Tests for the Postgres advisory lock."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_acquire_returns_lock_result_and_connects_once_with_keepalive() -> None:
        """Test acquire opens one keepalive-enabled session.

        Returns the pg_try_advisory_lock result.

        **Why this test is important:**
          - The single-writer lock rests on ``pg_try_advisory_lock`` mapping to the
            single-writer decision; and the long, idle hold requires TCP keepalives so a
            middlebox does not reap the session and silently release the lock. One pinned
            connection, reused across ticks.

        **What it tests:**
          - Two ``acquire()`` calls connect exactly once; the DSN passes through and
            keepalive ``server_settings`` are set; ``acquire()`` issues
            ``pg_try_advisory_lock`` and returns True.
        """
        conn = _mock_conn(lock_result=True)
        with patch("asyncpg.connect", AsyncMock(return_value=conn)) as connect:
            lock = PostgresAdvisoryLock(
                dsn="postgresql://u:p@h:5432/db?sslmode=require",
                key="kb:ds",
                namespace=7,
            )
            async with lock as entered:
                assert entered is lock
                assert await lock.acquire() is True
                assert (
                    await lock.acquire() is True
                )  # reuses the same session, no reconnect

        connect.assert_awaited_once()
        call = connect.call_args
        assert call.args[0] == "postgresql://u:p@h:5432/db?sslmode=require"
        assert "tcp_keepalives_idle" in call.kwargs["server_settings"]
        first_sql = cast(
            "list[object]",
            cast("list[object]", cast("AsyncMock", conn.fetchval).await_args_list)[
                0
            ].args[0],
        )
        assert "pg_try_advisory_lock" in first_sql

    @staticmethod
    @pytest.mark.asyncio
    async def test_release_issues_advisory_unlock_on_same_session() -> None:
        """Test release issues pg_advisory_unlock on the acquiring connection.

        **Why this test is important:**
          - Advisory locks are session-scoped; the unlock MUST run on the same connection
            that acquired, or the lock is never released and every other pod is starved.

        **What it tests:**
          - After acquire+release, a ``pg_advisory_unlock`` statement is issued on the
            connection.
        """
        conn = _mock_conn()
        with patch("asyncpg.connect", AsyncMock(return_value=conn)):
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                _ = await lock.acquire()
                await lock.release()

        issued = [
            cast("str", cast("list[object]", c.args)[0])
            for c in cast(
                "list[object]", cast("AsyncMock", conn.fetchval).await_args_list
            )
        ]
        assert any("pg_advisory_unlock" in sql for sql in issued)

    @staticmethod
    @pytest.mark.asyncio
    async def test_key_is_folded_into_signed_int32() -> None:
        """Test the advisory-lock key is (namespace, signed-int32 crc32 of the key).

        **Why this test is important:**
          - ``pg_try_advisory_lock(int4, int4)`` takes SIGNED 32-bit ints; ``zlib.crc32``
            is UNSIGNED 32-bit, so ~half of all keys exceed int4 max and would raise
            ``DataError`` at bind time (every acquire fails → the guarded work silently
            never runs). ``kb-1:ds-1`` is such a high-bit key. The namespace must pass
            through verbatim, or a consumer that keeps its lock identity across a rollout
            would stop excluding its older pods.

        **What it tests:**
          - For a key whose crc32 exceeds int32 max, the emitted objid is the
            signed-folded value; the classid is the given namespace.
        """
        conn = _mock_conn()
        with patch("asyncpg.connect", AsyncMock(return_value=conn)):
            lock = PostgresAdvisoryLock(
                dsn="postgresql://h/db", key="kb-1:ds-1", namespace=0x4B425359
            )
            async with lock:
                _ = await lock.acquire()

        _, classid, objid = cast(
            "tuple[object, object, float]",
            cast("list[object]", cast("AsyncMock", conn.fetchval).await_args_list)[
                0
            ].args,
        )
        raw = zlib.crc32(b"kb-1:ds-1")
        assert raw > INT32_MAX  # fixture actually exercises the overflow path
        assert objid == struct.unpack("i", struct.pack("I", raw))[0]
        assert INT32_MIN <= objid <= INT32_MAX
        assert classid == _EXPECTED_CLASSID

    @staticmethod
    def test_rejects_an_empty_key_or_an_out_of_range_namespace() -> None:
        """Test the lock refuses an empty key and a namespace outside signed int32.

        **Why this test is important:**
          - An empty key would make unrelated guards share one lock; an out-of-range
            namespace fails every acquire at bind time. Both must fail at construction,
            not at the first acquire.

        **What it tests:**
          - ``key=""``, ``namespace=2**31`` and ``namespace=-(2**31) - 1`` each raise
            ``ValueError``; both int32 bounds are accepted.
        """
        with pytest.raises(AppValueError, match="key"):
            _ = PostgresAdvisoryLock(dsn="postgresql://h/db", key="", namespace=1)
        with pytest.raises(AppValueError, match="namespace"):
            _ = PostgresAdvisoryLock(dsn="postgresql://h/db", key="k", namespace=2**31)
        with pytest.raises(AppValueError, match="namespace"):
            _ = PostgresAdvisoryLock(
                dsn="postgresql://h/db", key="k", namespace=INT32_MIN - 1
            )
        _ = PostgresAdvisoryLock(dsn="postgresql://h/db", key="k", namespace=INT32_MIN)
        _ = PostgresAdvisoryLock(dsn="postgresql://h/db", key="k", namespace=INT32_MAX)

    @staticmethod
    @pytest.mark.asyncio
    async def test_aexit_closes_the_session() -> None:
        """Test leaving the context closes the pinned connection.

        **Why this test is important:**
          - The lock owns one long-lived session; on shutdown the ``AsyncExitStack`` must
            close it so the pod drains cleanly and Postgres reclaims the backend.

        **What it tests:**
          - After ``async with`` exits, the connection's ``close`` was awaited.
        """
        conn = _mock_conn()
        with patch("asyncpg.connect", AsyncMock(return_value=conn)):
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                _ = await lock.acquire()

        cast("MagicMock", cast("AsyncMock", conn.close).assert_awaited_once)()

    @staticmethod
    @pytest.mark.asyncio
    async def test_connect_is_lazy_not_on_enter() -> None:
        """Test entering the context does NOT connect.

        The session opens on first acquire.

        **Why this test is important:**
          - The lock is entered into the caller's AsyncExitStack at startup; connecting
            eagerly would make the whole process's boot depend on Postgres even when the
            rest of its work does not. Connecting lazily on first ``acquire()`` decouples
            them.

        **What it tests:**
          - ``async with lock`` without calling ``acquire`` never calls
            ``asyncpg.connect``.
        """
        with patch("asyncpg.connect", AsyncMock(return_value=_mock_conn())) as connect:
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                pass

        connect.assert_not_awaited()

    @staticmethod
    @pytest.mark.asyncio
    async def test_acquire_reconnects_when_session_closed() -> None:
        """Test acquire reopens the session when the held connection has been closed.

        **Why this test is important:**
          - The pinned session can be reaped between acquires (idle timeout / crash).
            Without a reconnect the pod's guarded work wedges forever behind a dead socket
            while the caller keeps logging and continuing.

        **What it tests:**
          - When the current session reports ``is_closed()``, the next ``acquire`` opens a
            fresh connection (``asyncpg.connect`` awaited twice) and issues the lock on
            it.
        """
        dead, fresh = _mock_conn(), _mock_conn()
        dead.is_closed = MagicMock(return_value=True)  # reaped after the first round
        with patch("asyncpg.connect", AsyncMock(side_effect=[dead, fresh])) as connect:
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                _ = await lock.acquire()  # opens `dead`
                _ = await lock.acquire()  # `dead` is closed → reconnect to `fresh`

        assert connect.await_count == _EXPECTED_AWAIT_COUNT
        cast("MagicMock", cast("AsyncMock", fresh.fetchval).assert_awaited)()

    @staticmethod
    @pytest.mark.asyncio
    async def test_connection_failure_raises_transient_apperror() -> None:
        """Test a connection failure surfaces as a transient AppError.

        So the decorators can retry.

        **Why this test is important:**
          - The composition-root retry / circuit-breaker proxies act on
            ``AppError.is_transient``. A raw asyncpg ``InterfaceError`` would bypass them;
            mapping it to ``UnavailableError`` lets the resiliency stack engage instead of
            failing the round hard.

        **What it tests:**
          - When the session query raises an asyncpg connection error, ``acquire`` raises
            a transient ``UnavailableError``.
        """
        conn = _mock_conn()
        conn.fetchval = AsyncMock(
            side_effect=asyncpg.InterfaceError("connection is closed")
        )
        with patch("asyncpg.connect", AsyncMock(return_value=conn)):
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                with pytest.raises(UnavailableError) as excinfo:
                    _ = await lock.acquire()

        assert excinfo.value.is_transient is True

    @staticmethod
    @pytest.mark.asyncio
    async def test_release_swallows_a_broken_session() -> None:
        """Test release does not raise when the session is already broken.

        **Why this test is important:**
          - ``SingleWriterRunner`` calls ``release`` in a ``finally``; if it raised on a
            dead socket it would mask the run's real result. A dropped session already
            freed the advisory lock, so release must be a no-op on error.

        **What it tests:**
          - With a session whose unlock query raises an asyncpg error, ``release`` returns
            without propagating.
        """
        conn = _mock_conn()
        conn.fetchval = AsyncMock(
            side_effect=[True, asyncpg.InterfaceError("connection is closed")]
        )
        with patch("asyncpg.connect", AsyncMock(return_value=conn)):
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                assert await lock.acquire() is True
                await lock.release()  # must not raise

    @staticmethod
    @pytest.mark.asyncio
    async def test_acquire_terminates_broken_session_then_reconnects() -> None:
        """Test a connection error tears down the broken session.

        (freeing any held lock) and reconnects.

        **Why this test is important:**
          - If ``pg_try_advisory_lock`` took the lock server-side but the ack was lost,
            merely dropping the reference leaves a zombie session holding the lock until
            the keepalive reap — stalling the guarded work on every replica. Terminating
            the socket makes Postgres auto-release it at once.

        **What it tests:**
          - A connection error on acquire calls ``terminate()`` on the broken session and
            raises a transient error; the next acquire opens a fresh session and succeeds.
        """
        broken, fresh = _mock_conn(), _mock_conn()
        broken.fetchval = AsyncMock(
            side_effect=asyncpg.InterfaceError("connection reset")
        )
        with patch("asyncpg.connect", AsyncMock(side_effect=[broken, fresh])) as connect:
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                with pytest.raises(UnavailableError):
                    _ = (
                        await lock.acquire()
                    )  # broken session → terminate + transient raise
                cast(
                    "MagicMock", cast("MagicMock", broken.terminate).assert_called_once
                )()
                assert await lock.acquire() is True  # reconnects on a fresh session

        assert connect.await_count == _EXPECTED_AWAIT_COUNT

    @staticmethod
    @pytest.mark.asyncio
    async def test_release_warns_when_lock_not_held() -> None:
        """Test release logs a warning.

        When pg_advisory_unlock reports the lock was not held.

        **Why this test is important:**
          - A ``False`` unlock is a single-writer anomaly (e.g. the session was reset
            mid-hold); it must surface in the logs rather than pass silently.

        **What it tests:**
          - With unlock returning ``False``, ``release`` logs the "not held" warning
            carrying the lock's key and namespace (so a process holding several locks can
            tell which one), and does not raise.
        """
        conn = _mock_conn()
        conn.fetchval = AsyncMock(
            side_effect=[True, False]
        )  # acquire True, unlock reports not-held
        with (
            patch("asyncpg.connect", AsyncMock(return_value=conn)),
            patch("techai_webutils.clients.lock.postgres.lock.logger") as mock_logger,
        ):
            lock = PostgresAdvisoryLock(dsn="postgresql://h/db", key="kb:ds", namespace=7)
            async with lock:
                _ = await lock.acquire()
                await lock.release()

        fields = cast(
            "dict[str, object]",
            cast("MagicMock", cast("MagicMock", mock_logger.warning).call_args).kwargs,
        )
        assert (
            "not held"
            in cast(
                "tuple[str, ...]",
                cast("MagicMock", cast("MagicMock", mock_logger.warning).call_args).args,
            )[0]
        )
        assert fields["key"] == "kb:ds"
        assert fields["namespace"] == _EXPECTED_FIELDS


class TestLockFromConfig:
    """Tests for ``new_lock_from_config``."""

    @staticmethod
    def test_memory_kind_builds_in_memory_lock() -> None:
        """Test the factory builds the in-process lock for kind=memory.

        **Why this test is important:**
          - dev / replica=1 must never require Postgres; the memory kind is the safe
            default.

        **What it tests:**
          - ``new_lock_from_config`` with kind=memory returns an ``InMemoryLock``.
        """
        lock = new_lock_from_config(LockConfig(kind=LockKind.MEMORY))
        assert isinstance(lock, InMemoryLock)

    @staticmethod
    @pytest.mark.asyncio
    async def test_postgres_kind_locks_on_the_configured_identity() -> None:
        """Test the factory-built Postgres lock uses the config's key.

        Namespace and application name.

        **Why this test is important:**
          - The lock identity is (namespace, hash(key)). If the factory dropped or swapped
            either, old and new pods would lock different ids during a rollout and both
            run the single-writer job; the application name is how an operator finds the
            session in pg_stat_activity.

        **What it tests:**
          - ``new_lock_from_config`` with kind=postgres returns a ``PostgresAdvisoryLock``
            that acquires ``pg_try_advisory_lock(namespace, signed crc32(key))`` on a
            session tagged with the configured ``application_name``.
        """
        conn = _mock_conn()
        with patch("asyncpg.connect", AsyncMock(return_value=conn)) as connect:
            lock = new_lock_from_config(
                LockConfig(
                    kind=LockKind.POSTGRES,
                    key="kb:ds",
                    namespace=0x4B425359,
                    application_name="kb-sync",
                )
            )
            async with lock:
                _ = await lock.acquire()

        assert isinstance(lock, PostgresAdvisoryLock)
        _, classid, objid = cast(
            "tuple[object, object, object]",
            cast("list[object]", cast("AsyncMock", conn.fetchval).await_args_list)[
                0
            ].args,
        )
        assert classid == _EXPECTED_CLASSID
        assert objid == struct.unpack("i", struct.pack("I", zlib.crc32(b"kb:ds")))[0]
        assert (
            connect.call_args.kwargs["server_settings"]["application_name"] == "kb-sync"
        )

    @staticmethod
    def test_postgres_kind_requires_an_explicit_namespace() -> None:
        """Test the factory refuses a postgres lock config.

        That leaves the namespace unset.

        **Why this test is important:**
          - The namespace is half of the lock identity. A silent default of 0 let a
            consumer that forgot it lock a different id than its older pods (two writers
            at once) and share namespace 0 with every other consumer that forgot it,
            reintroducing the key-hash collisions it prevents.

        **What it tests:**
          - ``LockConfig(kind=postgres, key=...)`` with no namespace makes
            ``new_lock_from_config`` raise ``ValueError`` naming the namespace; the memory
            kind still needs none.
        """
        with pytest.raises(AppValueError, match="namespace"):
            _ = new_lock_from_config(LockConfig(kind=LockKind.POSTGRES, key="kb:ds"))
        assert isinstance(
            new_lock_from_config(LockConfig(kind=LockKind.MEMORY)), InMemoryLock
        )

    @staticmethod
    def test_unknown_kind_raises() -> None:
        """Test an unrecognised lock kind fails loudly (fail-fast on misconfiguration).

        **Why this test is important:**
          - A typo'd lock ``kind`` must crash at wiring, not silently fall through to no
            lock (which would drop the single-writer guarantee).

        **What it tests:**
          - ``new_lock_from_config`` with an unknown kind raises ``ValueError``.
        """
        # Deliberately bypass the LockKind enum with a bogus value to exercise the
        # defensive ``raise``.
        with pytest.raises(AppValueError, match="unknown lock kind"):
            _ = new_lock_from_config(
                LockConfig(kind=cast("LockKind", cast("object", "bogus")))
            )

    @staticmethod
    @pytest.mark.asyncio
    async def test_postgres_dsn_carries_sslmode_and_encodes_credentials() -> None:
        """Test the factory's Postgres DSN carries sslmode.

        And percent-encodes credentials.

        **Why this test is important:**
          - asyncpg has no ``sslmode`` kwarg — TLS must travel in the DSN URI, or
            staging/prod RDS (which requires TLS) refuses the connection. Special
            characters in the password must be encoded or the DSN is malformed.

        **What it tests:**
          - A postgres LockConfig with ``sslmode=require`` and a password containing ``@``
            and ``/`` produces a connection DSN with ``sslmode=require`` and the encoded
            password.
          - The public ``postgres_dsn`` returns that same DSN, so a pool opened beside the
            lock (the postgres start budget's) connects the same way.
        """
        config = LockConfig(
            kind=LockKind.POSTGRES,
            host="db",
            port=5432,
            user="app",
            password=_FAKE_CRED_1,
            database="knowledge_engine",
            sslmode="require",
            key="kb:ds",
            namespace=7,
        )
        conn = _mock_conn()
        with patch("asyncpg.connect", AsyncMock(return_value=conn)) as connect:
            lock = new_lock_from_config(config)
            async with lock:
                _ = await lock.acquire()

        dsn = cast("list[object]", connect.call_args.args[0])
        assert "sslmode=require" in dsn
        assert "p%40ss%2Fword" in dsn
        assert postgres_dsn(config) == dsn
