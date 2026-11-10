"""Integration tests for ``PostgresStartBudget`` against a real Postgres (testcontainer).

Each budget instance stands in for one pod: its slot locks are real
    ``PostgresAdvisoryLock``
sessions and its pacing runs against the published ``bedrock_start_rate`` DDL.
"""

from __future__ import annotations

import asyncio
import itertools
from contextlib import AsyncExitStack
from typing import TYPE_CHECKING, cast

import asyncpg
import pytest
import pytest_asyncio

from techai_webutils.clients.lock.postgres import PostgresAdvisoryLock
from techai_webutils.clients.start_budget import (
    StartBudgetConfig,
    StartBudgetKind,
    start_budget_from_config,
)
from techai_webutils.core.errors import InternalError

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

    from techai_webutils.core.interfaces.start_budget import Slot, StartBudget

_EXPECTED_SLOTS_COUNT = 5

pytestmark = pytest.mark.integration

_DDL = (
    "CREATE TABLE IF NOT EXISTS bedrock_start_rate ("
    "account text PRIMARY KEY, last_start_at timestamptz NOT NULL)"
)
_INTERVAL = 0.3

_STARTED_SQL = "SELECT last_start_at > 'epoch' FROM bedrock_start_rate WHERE account = $1"
"""Whether the account has recorded a start since the epoch."""


@pytest_asyncio.fixture
async def pool(postgres_dsn: str) -> AsyncIterator[asyncpg.Pool]:
    """Yield a pool on a database holding an empty ``bedrock_start_rate``.

    Args:
        postgres_dsn: A libpq DSN for the module's real Postgres.

    Yields:
        A pool on a database holding an empty ``bedrock_start_rate``.

    """
    pool = await asyncpg.create_pool(postgres_dsn, min_size=1, max_size=8)
    _ = await pool.execute(_DDL)
    _ = await pool.execute("TRUNCATE bedrock_start_rate")
    try:
        yield pool
    finally:
        await pool.close()


def _budget(pool: asyncpg.Pool, dsn: str, timeout: float) -> StartBudget:
    """Build one pod's postgres budget: 5 slots, a 0.3 s interval.

    ``timeout`` to acquire.

    Args:
        pool: A pool on a database holding an empty ``bedrock_start_rate``.
        dsn: The database's DSN.
        timeout: Each call's deadline in seconds.

    Returns:
        One pod's postgres budget: 5 slots, a 0.3 s interval.

    """
    config = StartBudgetConfig(
        kind=StartBudgetKind.POSTGRES,
        account="acct-1",
        min_start_interval_seconds=_INTERVAL,
        acquire_timeout_seconds=timeout,
        poll_interval_seconds=0.1,
    )
    return start_budget_from_config(
        config,
        pool=pool,
        new_lock=lambda key: PostgresAdvisoryLock(dsn=dsn, key=key, namespace=7),
    )


@pytest.mark.asyncio
async def test_six_pods_five_hold_and_starts_are_paced(
    pool: asyncpg.Pool, postgres_dsn: str
) -> None:
    """Test that six concurrent acquirers get five slots, starts are an interval apart.

    And a release frees one.

    **Why this test is important:**
      - Bedrock caps the account's concurrent ingestion jobs and rejects bursts of starts;
        the budget is what keeps every pod inside both limits together.

    **What it tests:**
      - six budgets acquiring at once: five get distinct slots, the sixth gets ``None`` at
        its timeout
      - the five grants span at least four 0.3 s intervals, and no two are less than half
        an interval apart (the interval is kept by the database clock; the grants are
        timed when each acquire returns, a few milliseconds after its commit)
      - after one holder releases, a new acquire gets that slot

    Args:
        pool: A pool on a database holding an empty ``bedrock_start_rate``.
        postgres_dsn: A libpq DSN for the module's real Postgres.

    """
    loop = asyncio.get_running_loop()
    granted: list[tuple[float, Slot | None]] = []

    async def take(budget: StartBudget, n: int) -> None:
        """Acquire a start slot and record when it was granted.

        Args:
            budget: The pod's start budget.
            n: The data source's number.

        """
        slot = await budget.acquire(f"kb:ds-{n}")
        granted.append((loop.time(), slot))

    async with AsyncExitStack() as stack:
        budgets = [
            await stack.enter_async_context(_budget(pool, postgres_dsn, timeout=3.0))
            for _ in range(7)
        ]
        _ = await asyncio.gather(*(take(b, n) for n, b in enumerate(budgets[:6])))

        slots = [slot for _, slot in granted if slot is not None]
        assert len(slots) == _EXPECTED_SLOTS_COUNT
        assert sorted(slot.index for slot in slots) == [0, 1, 2, 3, 4]
        times = sorted(t for t, slot in granted if slot is not None)
        assert times[-1] - times[0] >= 4 * _INTERVAL * 0.95
        assert all(b - a >= _INTERVAL * 0.5 for a, b in itertools.pairwise(times))

        held = slots[0]
        await budgets[int(held.binding.rsplit("-", 1)[1])].release(held)
        again = await budgets[6].acquire("kb:late")
        assert again is not None
        assert again.index == held.index


@pytest.mark.asyncio
async def test_a_budget_built_from_a_dsn_paces_through_its_own_pool(
    pool: asyncpg.Pool, postgres_dsn: str
) -> None:
    """Test that a budget given a DSN instead of a pool opens.

    Uses and closes its own pool.

    **Why this test is important:**
      - A product composition root passes the database's DSN rather than an asyncpg pool,
        so the pool the pacing needs must be the budget's own, opened on entry and closed
        on exit.

    **What it tests:**
      - inside its context, the DSN-built budget grants a slot and stamps the account's
        row
      - after its exit, the budget refuses to pace, since its pool is closed

    Args:
        pool: A pool on a database holding an empty ``bedrock_start_rate``.
        postgres_dsn: A libpq DSN for the module's real Postgres.

    """
    config = StartBudgetConfig(
        kind=StartBudgetKind.POSTGRES,
        account="acct-dsn",
        min_start_interval_seconds=_INTERVAL,
        acquire_timeout_seconds=3.0,
        poll_interval_seconds=0.1,
    )
    budget = start_budget_from_config(
        config,
        dsn=postgres_dsn,
        new_lock=lambda key: PostgresAdvisoryLock(dsn=postgres_dsn, key=key, namespace=8),
    )
    async with budget:
        slot = await budget.acquire("kb:ds")
        assert slot is not None
        stamped = cast(
            "object",
            await pool.fetchval(
                _STARTED_SQL,
                "acct-dsn",
            ),
        )
        assert stamped is True
    with pytest.raises(InternalError):
        _ = await budget.acquire("kb:ds")
