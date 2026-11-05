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
) -> StartBudget:
    """Build the ``StartBudget`` ``config.kind`` selects.

    The ``postgres`` kind needs ``pool`` (for the start-rate row) and ``new_lock``, which returns
    the cross-pod lock for a slot key; the composition root passes the postgres ``LockConfig`` with
    that key to ``new_lock_from_config``.

    Raises:
        AppValueError: An unknown kind, a missing ``pool`` / ``new_lock`` / ``account``, or a
            non-positive ``slots`` or timeout, or a negative interval (``INVALID_INPUT``).

    """
    if config.kind == StartBudgetKind.NOOP:
        return NoopStartBudget()
    if config.kind == StartBudgetKind.POSTGRES:
        _validate(config)
        if pool is None or new_lock is None:
            msg = "the postgres start budget needs a pool and a lock factory"
            raise AppValueError(msg)
        return PostgresStartBudget(config, pool=pool, new_lock=new_lock)
    msg = f"unknown start budget kind: {config.kind!r}"
    raise AppValueError(msg)


def _validate(config: StartBudgetConfig) -> None:
    """Reject a postgres config whose limits cannot work."""
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
