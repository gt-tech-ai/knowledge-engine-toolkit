"""``start_budget_from_config``: the config-selected ``StartBudget``."""

from __future__ import annotations

from typing import TYPE_CHECKING

from techai_webutils.clients.start_budget.config import StartBudgetConfig, StartBudgetKind
from techai_webutils.clients.start_budget.noop import NoopStartBudget
from techai_webutils.clients.start_budget.postgres import PostgresStartBudget
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Callable

    import asyncpg

    from techai_webutils.core.interfaces.lock import ManagedLock
    from techai_webutils.core.interfaces.start_budget import StartBudget


def start_budget_from_config(
    config: StartBudgetConfig,
    *,
    pool: asyncpg.Pool | None = None,
    new_lock: Callable[[str], ManagedLock] | None = None,
    dsn: str = "",
) -> StartBudget:
    """Build the ``StartBudget`` ``config.kind`` selects.

    The ``postgres`` kind needs the start-rate row's database, as a ``pool`` or a ``dsn``
    (the budget then opens and closes its own pool, ``clients.lock.postgres_dsn`` building
    it from the lock's config), and ``new_lock``, which returns the cross-pod lock for a
    slot key; the composition root passes the postgres ``LockConfig`` with that key to
    ``new_lock_from_config``.

    Args:
        config: Selects the backend and carries its settings.
        pool: A shared asyncpg pool for the postgres kind.
        new_lock: Builds a slot's lock for the postgres kind.
        dsn: The database the postgres kind opens a pool on when ``pool`` is ``None``.

    Returns:
        The ``StartBudget`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, a missing ``pool`` and ``dsn``, ``new_lock`` or
            ``account``, or a non-positive ``slots`` or timeout, or a negative interval
            (``INVALID_INPUT``).

    """
    if config.kind == StartBudgetKind.NOOP:
        return NoopStartBudget()
    if config.kind == StartBudgetKind.POSTGRES:
        _validate(config)
        if (pool is None and not dsn) or new_lock is None:
            msg = "the postgres start budget needs a pool or a dsn, and a lock factory"
            raise AppValueError(msg)
        return PostgresStartBudget(config, new_lock=new_lock, pool=pool, dsn=dsn)
    msg = f"unknown start budget kind: {config.kind!r}"
    raise AppValueError(msg)


def _validate(config: StartBudgetConfig) -> None:
    """Reject a postgres config whose limits cannot work.

    Args:
        config: The budget's settings.

    Raises:
        AppValueError: The postgres start budget needs an account; start budget slots must
            be at least 1, got ``config.slots``; start budget acquire timeout and poll
            interval must be positive; start budget minimum start interval must not be
            negative.

    """
    if not config.account:
        msg = "the postgres start budget needs an account"
        raise AppValueError(msg)
    if config.slots < 1:
        msg = f"start budget slots must be at least 1, got {config.slots}"
        raise AppValueError(msg)
    if config.acquire_timeout_seconds <= 0 or config.poll_interval_seconds <= 0:
        msg = "start budget acquire timeout and poll interval must be positive"
        raise AppValueError(msg)
    if config.min_start_interval_seconds < 0:
        msg = "start budget minimum start interval must not be negative"
        raise AppValueError(msg)
