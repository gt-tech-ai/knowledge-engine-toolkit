"""Budget enforcement policy: what the token-accounting decorator does with a ``BudgetDecision``.

``Enforcement.SHADOW`` (the default) serves an over-budget scope and counts it
(``ai_budget_would_exceed_total``), so a budget can be dual-run before it bites; ``SOFT`` and
``HARD`` reject an exhausted scope with ``QuotaExceededError`` before the model call. A ledger
outage always serves (fail-open).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class Enforcement(StrEnum):
    """How an exhausted budget is enforced."""

    SHADOW = "shadow"
    """Serve and count; never reject (the default)."""
    SOFT = "soft"
    """Reject an exhausted budget; a ledger outage serves."""
    HARD = "hard"
    """Reject an exhausted budget."""


@dataclass(frozen=True, slots=True)
class BudgetPolicy:
    """The budget enforcement the token-accounting decorator applies."""

    enforcement: Enforcement = Enforcement.SHADOW
    """How an exhausted budget is enforced."""
