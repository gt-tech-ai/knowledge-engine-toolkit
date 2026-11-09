"""``PostgresStartBudget``: slots are advisory locks.

The start interval is one row per account.

Slot ``i`` is the lock ``new_lock("<account>:<i>")`` (a ``ManagedLock``, the postgres Kind
of ``clients.lock`` in production), held by its own session for as long as the job runs; a
process that dies drops its sessions and Postgres frees its slots. Each lock is opened
once and kept, so a waiter's retries reuse its sessions instead of reconnecting.

The start interval lives in ``bedrock_start_rate(account text primary key, last_start_at
timestamptz not null)``. After taking a slot, ``acquire`` locks the account's row, waits
until ``last_start_at + interval`` by the database clock, stamps the row and commits, so
starts across every pod are at least the interval apart. A waiter blocked on the row lock
may overrun ``acquire_timeout_seconds`` by up to one interval.

The pacing pool is injected, or opened from a DSN on entry: an owned pool opens no
    connection
until a start is paced (``min_size`` 0), and each of its statements is bounded by the
longest a pacing start can wait on another pod's row lock (the interval plus the acquire
timeout).
"""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, cast, final, override

import asyncpg

from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.start_budget import Slot, StartBudget

if TYPE_CHECKING:
    from collections.abc import Callable
    from types import TracebackType
    from typing import Self

    from techai_webutils.clients.start_budget.config import StartBudgetConfig
    from techai_webutils.core.interfaces.lock import ManagedLock

_SEED = (
    "INSERT INTO bedrock_start_rate (account, last_start_at) VALUES ($1, 'epoch') "
    "ON CONFLICT (account) DO NOTHING"
)
"""Create the account's row on first use, as if it had never started a job."""
_WAIT = (
    "SELECT EXTRACT(EPOCH FROM (last_start_at + make_interval(secs => $2) - "
    "clock_timestamp()))"
    "::float8 FROM bedrock_start_rate WHERE account = $1 FOR UPDATE"
)
"""Lock the account's row and return the seconds until the next start is allowed (≤ 0:
now)."""
_STAMP = (
    "UPDATE bedrock_start_rate SET last_start_at = clock_timestamp() WHERE account = $1"
)
"""Record a start now."""
_OWNED_POOL_MAX = 2
"""Connections an owned pacing pool may open: a pacing start holds one through its
wait."""


@final
class PostgresStartBudget(StartBudget):
    """Account-wide slots and start pacing in Postgres, shared by every pod."""

    def __init__(
        self,
        config: StartBudgetConfig,
        *,
        new_lock: Callable[[str], ManagedLock],
        pool: asyncpg.Pool | None = None,
        dsn: str = "",
    ) -> None:
        """Pace through ``pool``.

        (or a pool opened on ``dsn`` at entry) and take slot ``i`` with
        ``new_lock("<account>:<i>")``.
        """
        self._config = config
        self._pool = pool
        self._dsn = dsn
        self._owns_pool = pool is None
        self._new_lock = new_lock
        self._locks: dict[int, ManagedLock] = {}
        self._held: set[int] = set()
        self._scan = asyncio.Lock()
        self._stack = AsyncExitStack()

    @override
    async def acquire(self, binding: str) -> Slot | None:
        """Take a free slot, then wait for the start interval.

        ``None`` once the wait times out.

        Returns:
            The slot, or ``None`` when none became available.

        """
        loop = asyncio.get_running_loop()
        deadline = loop.time() + self._config.acquire_timeout_seconds
        while True:
            index = await self._try_slots()
            if index is not None:
                if await self._pace(deadline):
                    return Slot(binding=binding, index=index)
                await self._free(index)
                return None
            if loop.time() >= deadline:
                return None
            await asyncio.sleep(self._config.poll_interval_seconds)

    @override
    async def release(self, slot: Slot) -> None:
        """Unlock ``slot`` if this budget holds it.

        The session stays open for the next acquire.
        """
        if slot.index in self._held:
            await self._free(slot.index)

    @override
    async def __aenter__(self) -> Self:
        """Enter the async context, opening the owned pacing pool.

        Lock sessions open on first use.

        Returns:
            This instance.

        """
        if self._owns_pool:
            self._pool = await self._stack.enter_async_context(
                asyncpg.create_pool(
                    self._dsn,
                    min_size=0,
                    max_size=_OWNED_POOL_MAX,
                    command_timeout=self._config.min_start_interval_seconds
                    + self._config.acquire_timeout_seconds,
                )
            )
        return self

    @override
    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Free every held slot and close every lock session.

        (and the owned pacing pool).
        """
        self._locks = {}
        self._held.clear()
        if self._owns_pool:
            self._pool = None
        stack, self._stack = self._stack, AsyncExitStack()
        await stack.aclose()

    async def _try_slots(self) -> int | None:
        """Try each slot this process does not hold; return the first one won.

        Else ``None``.

        The scan is serialized within the process: an advisory lock is re-entrant in its
            session, so
        two coroutines must never try the same slot's lock at once.

        Returns:
            The slot won, or ``None``.

        """
        async with self._scan:
            for index in range(self._config.slots):
                if index in self._held:
                    continue
                if await (await self._lock(index)).acquire():
                    self._held.add(index)
                    return index
        return None

    async def _lock(self, index: int) -> ManagedLock:
        """Return slot ``index``'s lock, opening its session on first use.

        Returns:
            Slot ``index``'s lock, opening its session on first use.

        """
        lock = self._locks.get(index)
        if lock is None:
            lock = self._new_lock(f"{self._config.account}:{index}")
            _ = await self._stack.enter_async_context(lock)
            self._locks[index] = lock
        return lock

    async def _free(self, index: int) -> None:
        """Unlock slot ``index``."""
        self._held.discard(index)
        await self._locks[index].release()

    async def _pace(self, deadline: float) -> bool:
        """Wait until a start is allowed and record it.

        ``False`` if that is past ``deadline``.

        Returns:
            Whether the start may proceed.

        Raises:
            InternalError: The postgres start budget paces only inside its async context.

        """
        account = self._config.account
        loop = asyncio.get_running_loop()
        if self._pool is None:
            msg = "the postgres start budget paces only inside its async context"
            raise InternalError(msg)
        async with self._pool.acquire() as conn, conn.transaction():
            _ = await conn.execute(_SEED, account)
            wait = float(
                cast(
                    "float",
                    await conn.fetchval(
                        _WAIT, account, self._config.min_start_interval_seconds
                    ),
                )
            )
            if wait > 0:
                if loop.time() + wait > deadline:
                    return False
                await asyncio.sleep(wait)
            _ = await conn.execute(_STAMP, account)
        return True
