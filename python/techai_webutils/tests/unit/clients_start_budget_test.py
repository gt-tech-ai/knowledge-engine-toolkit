"""Unit tests for the start budgets and their factory.

The locks and the asyncpg pool are mocked.
"""

from __future__ import annotations

from dataclasses import replace
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.start_budget import (
    StartBudgetConfig,
    StartBudgetKind,
    start_budget_from_config,
)
from techai_webutils.clients.start_budget.noop import NoopStartBudget
from techai_webutils.clients.start_budget.postgres import PostgresStartBudget
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.lock import ManagedLock
from techai_webutils.core.interfaces.start_budget import Slot

if TYPE_CHECKING:
    from collections.abc import Sequence

_CONFIG = StartBudgetConfig(
    kind=StartBudgetKind.POSTGRES,
    account="acct",
    slots=2,
    min_start_interval_seconds=10.0,
    acquire_timeout_seconds=0.05,
    poll_interval_seconds=0.01,
)


def _cm(value: object) -> MagicMock:
    """Return an async context manager mock that yields ``value``.

    Args:
        value: What entering it returns.

    Returns:
        An async context manager mock that yields ``value``.

    """
    cm = MagicMock()
    cm.__aenter__ = AsyncMock(return_value=value)
    cm.__aexit__ = AsyncMock(return_value=None)
    return cm


def _pool(wait: float = 0.0) -> tuple[MagicMock, MagicMock]:
    """Return a mocked pool and its connection.

    The rate query reports ``wait`` seconds to go.

    Args:
        wait: Seconds before the pool hands out a connection.

    Returns:
        A mocked pool and its connection.

    """
    conn = MagicMock()
    conn.execute = AsyncMock()
    conn.fetchval = AsyncMock(return_value=wait)
    conn.transaction = MagicMock(return_value=_cm(None))
    pool = MagicMock()
    pool.acquire = MagicMock(return_value=_cm(conn))
    return pool, conn


def _locks(wins: Sequence[bool] = ()) -> tuple[dict[str, MagicMock], MagicMock]:
    """Return the locks a factory hands out by key, and the factory.

    Each lock wins per ``wins``.

    Args:
        wins: Whether each lock is won, in order.

    Returns:
        The locks a factory hands out by key, each winning per ``wins``.

    """
    made: dict[str, MagicMock] = {}
    results = iter(wins)

    def new_lock(key: str) -> MagicMock:
        lock = MagicMock(spec=ManagedLock)
        lock.__aenter__ = AsyncMock(return_value=lock)
        lock.__aexit__ = AsyncMock(return_value=None)
        lock.acquire = AsyncMock(return_value=next(results))
        lock.release = AsyncMock()
        made[key] = lock
        return lock

    return made, MagicMock(side_effect=new_lock)


def _budget(
    pool: MagicMock, factory: MagicMock, config: StartBudgetConfig = _CONFIG
) -> PostgresStartBudget:
    """Build the postgres budget through the factory.

    Args:
        pool: The connection pool.
        factory: Builds each lock.
        config: The budget settings.

    Returns:
        The postgres budget through the factory.

    """
    budget = start_budget_from_config(config, pool=pool, new_lock=factory)
    assert isinstance(budget, PostgresStartBudget)
    return budget


@pytest.mark.asyncio
async def test_default_kind_is_noop_and_always_grants() -> None:
    """Test that the default config grants every acquire at once.

    **Why this test is important:**
      - The default must keep today's behaviour and cost: no database, no waiting.

    **What it tests:**
      - ``StartBudgetConfig()`` builds ``NoopStartBudget``; acquire returns slot 0 with
        the binding
    """
    budget = start_budget_from_config(StartBudgetConfig())

    assert isinstance(budget, NoopStartBudget)
    slot = await budget.acquire("kb:ds")
    assert slot == Slot(binding="kb:ds", index=0)
    await budget.release(Slot(binding="kb:ds", index=0))


@pytest.mark.parametrize(
    "config",
    [
        replace(_CONFIG, account=""),
        replace(_CONFIG, slots=0),
        replace(_CONFIG, acquire_timeout_seconds=0),
        replace(_CONFIG, poll_interval_seconds=0),
        replace(_CONFIG, min_start_interval_seconds=-1),
    ],
)
def test_postgres_rejects_unworkable_limits(config: StartBudgetConfig) -> None:
    """Test that the factory refuses a postgres config whose limits cannot work.

    **Why this test is important:**
      - A budget with no account or no slots would block every start or share a row by
        accident; it must fail at boot.

    **What it tests:**
      - an empty account, zero slots, a non-positive timeout or poll, a negative interval
        raise ``AppValueError``

    Args:
        config: The budget settings.

    """
    pool, _ = _pool()
    _, factory = _locks()
    with pytest.raises(AppValueError):
        _ = start_budget_from_config(config, pool=pool, new_lock=factory)


def test_postgres_needs_pool_and_lock_factory_and_unknown_kind_raises() -> None:
    """Test that the factory fails loudly on missing collaborators or an unknown kind.

    **Why this test is important:**
      - A misconfigured budget must stop the app at boot rather than run unbounded.

    **What it tests:**
      - postgres without ``pool`` or ``new_lock`` raises ``AppValueError``; so does an
        unknown kind
    """
    with pytest.raises(AppValueError):
        _ = start_budget_from_config(_CONFIG)
    bogus = MagicMock(spec=StartBudgetConfig)
    bogus.kind = "bogus"
    with pytest.raises(AppValueError):
        _ = start_budget_from_config(bogus)


@pytest.mark.asyncio
async def test_acquire_takes_first_free_slot_and_skips_held_ones() -> None:
    """Test that acquire wins the first free slot and records the start.

    A second acquire skips it.

    **Why this test is important:**
      - An advisory lock is re-entrant in its session, so the budget itself must never
        hand one process's held slot out twice.

    **What it tests:**
      - the first acquire locks ``acct:0`` and stamps the rate row; the second locks
        ``acct:1``
      - releasing slot 0 unlocks it; releasing it again does nothing
    """
    pool, conn = _pool()
    locks, factory = _locks([True, True])
    budget = _budget(pool, factory)

    first = await budget.acquire("a")
    second = await budget.acquire("b")

    assert first == Slot(binding="a", index=0)
    assert second == Slot(binding="b", index=1)
    assert [c.args[0] for c in factory.call_args_list] == ["acct:0", "acct:1"]
    stamp = cast("MagicMock", cast("AsyncMock", conn.execute).await_args_list[-1])
    assert cast("MagicMock", cast("list[object]", stamp.args)[0].startswith)(
        "UPDATE bedrock_start_rate"
    )
    assert cast("list[object]", stamp.args)[1] == "acct"
    assert first is not None
    await budget.release(first)
    await budget.release(first)
    cast("MagicMock", cast("AsyncMock", locks["acct:0"].release).assert_awaited_once)()


@pytest.mark.asyncio
async def test_acquire_returns_none_when_no_slot_frees_in_time() -> None:
    """Test that acquire gives up at its timeout when every slot is held elsewhere.

    **Why this test is important:**
      - A caller must get control back to skip the round instead of blocking forever.

    **What it tests:**
      - with both slot locks lost on every try, acquire returns ``None`` and never paces
    """
    pool, conn = _pool()
    _, factory = _locks([False] * 100)
    budget = _budget(pool, factory)

    assert await budget.acquire("a") is None
    cast("MagicMock", cast("AsyncMock", conn.execute).assert_not_awaited)()


@pytest.mark.asyncio
async def test_pacing_past_deadline_frees_the_slot() -> None:
    """Test that a start interval longer than the remaining wait frees the slot.

    It returns ``None``.

    **Why this test is important:**
      - A slot held by a caller that will not start would starve the account.

    **What it tests:**
      - the rate query reports 100 s to go; acquire returns ``None``, unlocks the slot,
        never stamps
    """
    pool, conn = _pool(wait=100.0)
    locks, factory = _locks([True])
    budget = _budget(pool, factory)

    assert await budget.acquire("a") is None
    cast("MagicMock", cast("AsyncMock", locks["acct:0"].release).assert_awaited_once)()
    assert all(
        not cast("MagicMock", c.args[0].startswith)("UPDATE")
        for c in cast("list[object]", cast("AsyncMock", conn.execute).await_args_list)
    )


@pytest.mark.asyncio
async def test_pacing_waits_then_stamps_and_exit_closes_sessions() -> None:
    """Test that a short remaining interval is waited out.

    Leaving the context closes every lock.

    **Why this test is important:**
      - Pacing is what keeps starts inside Bedrock's rate; closing the sessions frees
        every slot at shutdown.

    **What it tests:**
      - with 0.01 s to go, acquire returns slot 0 after stamping the row
      - ``__aexit__`` exits every opened lock
    """
    pool, conn = _pool(wait=0.01)
    locks, factory = _locks([True])

    async with _budget(
        pool, factory, replace(_CONFIG, acquire_timeout_seconds=5)
    ) as budget:
        assert await budget.acquire("a") == Slot(binding="a", index=0)
        assert cast(
            "MagicMock",
            cast(
                "list[object]",
                cast("list[object]", cast("AsyncMock", conn.execute).await_args_list)[
                    -1
                ].args,
            )[0].startswith,
        )("UPDATE")

    cast("MagicMock", cast("AsyncMock", locks["acct:0"].__aexit__).assert_awaited_once)()
