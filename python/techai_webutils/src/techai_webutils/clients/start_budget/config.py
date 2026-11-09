"""``StartBudgetConfig``: which start budget to build and its slots.

Interval and waits.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class StartBudgetKind(StrEnum):
    """Which start budget to build."""

    NOOP = "noop"
    """Every acquire succeeds at once: no account-wide cap (today's behaviour)."""
    POSTGRES = "postgres"
    """Slots are Postgres advisory locks and starts are paced through
    ``bedrock_start_rate``."""


@dataclass(frozen=True)
class StartBudgetConfig:
    """Selects the start budget and, for ``postgres``, the account's limits."""

    kind: StartBudgetKind = StartBudgetKind.NOOP
    """The budget to build; ``noop`` by default."""
    account: str = ""
    """The AWS account the budget covers; it names the slot locks and the rate row
    (``postgres``)."""
    slots: int = 5
    """How many ingestion jobs may run at once in the account."""
    min_start_interval_seconds: float = 10.0
    """The least time between two job starts in the account."""
    acquire_timeout_seconds: float = 300.0
    """How long ``acquire`` waits for a slot and the interval before returning
    ``None``."""
    poll_interval_seconds: float = 1.0
    """How long a waiter sleeps between two scans of the slots."""
