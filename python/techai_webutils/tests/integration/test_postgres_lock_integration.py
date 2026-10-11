"""Integration tests for PostgresAdvisoryLock against a real Postgres (testcontainer).

Unit tests drive the lock against a mocked asyncpg connection; these prove the properties that only
real Postgres can demonstrate: cross-session mutual exclusion (one holder blocks contenders and a
``SingleWriterRunner`` skips), the namespace as a separate half of the lock identity, and the
crash-safe, no-TTL auto-release when the holding session is dropped. The ``postgres_dsn`` fixture runs
a ``postgres:16-alpine`` testcontainer.
"""

from __future__ import annotations

import asyncio

import pytest

from techai_webutils.clients.lock import SingleWriterRunner
from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock


def _pair(dsn: str, key: str) -> tuple[PostgresAdvisoryLock, PostgresAdvisoryLock]:
    """Build two locks over separate sessions that contend on the same key."""
    return (
        PostgresAdvisoryLock(dsn=dsn, key=key, namespace=1),
        PostgresAdvisoryLock(dsn=dsn, key=key, namespace=1),
    )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_single_writer_runner_serializes_across_sessions(postgres_dsn: str) -> None:
    """Test one holder blocks a contender and a SingleWriterRunner skips while the lock is held.

    **Why this test is important:**
      - This is the invariant the single-writer lock exists for: with several replicas, exactly one may
        drive the guarded job. Two live sessions on one DB must not both acquire the same advisory lock.

    **What it tests:**
      - While session A holds the lock, a SingleWriterRunner over session B returns None without running
        the op; once A releases, B's runner runs.
    """
    a, b = _pair(postgres_dsn, "kb-serialize")
    async with a, b:
        ran: list[str] = []

        async def op() -> str:
            await asyncio.sleep(0)
            ran.append("b")
            return "b"

        assert await a.acquire() is True
        assert await SingleWriterRunner(op, b).run() is None  # B skips while A holds
        assert ran == []

        await a.release()
        assert await SingleWriterRunner(op, b).run() == "b"  # A released → B runs
        assert ran == ["b"]
        await b.release()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_dropped_session_auto_releases_the_lock(postgres_dsn: str) -> None:
    """Test dropping the holding session auto-releases the advisory lock (crash-safe, no TTL).

    **Why this test is important:**
      - Session-scoped auto-release is the point of a Postgres advisory lock over a TTL lock: a crashed
        holder frees the lock for free, with no lease to size against however long the guarded work runs.

    **What it tests:**
      - With A holding the lock, B is blocked; after A's connection is closed (a simulated crash, no
        explicit release), B acquires successfully.
    """
    a, b = _pair(postgres_dsn, "kb-crash")
    async with a, b:
        assert await a.acquire() is True
        assert await b.acquire() is False  # blocked while A holds
        await a.aclose()  # crash: drop A's session with the lock still held
        assert await b.acquire() is True  # Postgres auto-released it — B wins, no TTL
        await b.release()


@pytest.mark.integration
@pytest.mark.asyncio
async def test_same_key_in_different_namespaces_does_not_contend(
    postgres_dsn: str,
) -> None:
    """Test the namespace is its own half of the lock identity: one key under two namespaces never contends.

    **Why this test is important:**
      - The namespace exists so one lock class cannot collide with another that hashes the same key.
        Only real advisory locks prove it is sent as the separate ``classid`` rather than folded into
        the key hash (a regression there would serialize unrelated guards, or split one guard).

    **What it tests:**
      - While a namespace-1 lock on "kb-ns" is held, a namespace-2 lock on "kb-ns" acquires, and a second
        namespace-1 lock on "kb-ns" is still blocked.
    """
    held = PostgresAdvisoryLock(dsn=postgres_dsn, key="kb-ns", namespace=1)
    other_namespace = PostgresAdvisoryLock(dsn=postgres_dsn, key="kb-ns", namespace=2)
    same_namespace = PostgresAdvisoryLock(dsn=postgres_dsn, key="kb-ns", namespace=1)
    async with held, other_namespace, same_namespace:
        assert await held.acquire() is True
        assert (
            await other_namespace.acquire() is True
        )  # another namespace: a different lock
        assert await same_namespace.acquire() is False  # same identity: blocked
        await other_namespace.release()
        await held.release()
