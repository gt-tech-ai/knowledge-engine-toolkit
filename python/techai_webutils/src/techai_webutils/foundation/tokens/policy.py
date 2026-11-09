"""Budget enforcement policy.

What the token-accounting decorator does with a ``BudgetDecision``.

``decide`` is the whole table:

| enforcement | within budget | exhausted            | ledger error                     |
|-------------|---------------|----------------------|----------------------------------|
| ``shadow``  | serve         | serve, would-exceed  | serve                            |
| ``soft``    | serve         | reject               | serve (fail open)                |
| ``hard``    | serve         | reject               | reject (fail closed)             |

A ledger error is a raised ``check_budget`` *or* a decision whose reason is
``LEDGER_UNAVAILABLE_REASON`` (a backend that failed open itself). ``on_ledger_error``
overrides the soft/hard default; ``shadow`` never rejects. With ``per_budget`` set, an
exhausted budget uses the enforcement its owner stored on it
(``BudgetDecision.enforcement``) when the decision carries one; ledger errors keep the
policy's own. A rejection is a coded ``QuotaExceededError`` raised by the caller, before
the model call.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.interfaces.token_ledger import (
    LEDGER_UNAVAILABLE_REASON,
    Enforcement,
)

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.token_ledger import BudgetDecision

__all__ = ["BudgetPolicy", "Enforcement", "OnLedgerError", "Verdict", "decide"]
"""``Enforcement`` is re-exported from core, so importers of this module keep working."""


class OnLedgerError(StrEnum):
    """What a ledger error does under ``soft`` or ``hard`` enforcement."""

    ALLOW = "allow"
    """Serve (fail open)."""
    DENY = "deny"
    """Reject (fail closed)."""


@dataclass(frozen=True, slots=True)
class BudgetPolicy:
    """The budget enforcement the token-accounting decorator applies."""

    enforcement: Enforcement = Enforcement.SHADOW
    """How an exhausted budget is enforced."""
    on_ledger_error: OnLedgerError | None = None
    """What a ledger error does; ``None`` takes the enforcement's default (``hard``
    denies, ``soft`` allows)."""
    per_budget: bool = False
    """Whether an exhausted budget is enforced at the level its owner stored on it
    (``BudgetDecision.enforcement``) instead of ``enforcement``; a decision without a
    level, and every ledger error, still follow ``enforcement``."""

    def ledger_error_action(self) -> OnLedgerError:
        """Return what a ledger error does under this policy."""
        if self.enforcement is Enforcement.SHADOW:
            return OnLedgerError.ALLOW
        if self.on_ledger_error is not None:
            return self.on_ledger_error
        return (
            OnLedgerError.DENY
            if self.enforcement is Enforcement.HARD
            else OnLedgerError.ALLOW
        )


@dataclass(frozen=True, slots=True)
class Verdict:
    """What to do with one call."""

    serve: bool
    """Whether the call may proceed."""
    would_exceed: bool
    """Whether the budget is exhausted (counted under ``shadow``; the rejection
    otherwise)."""
    reason: str
    """Why: the decision's reason, ``ledger_error_fail_open`` or
    ``ledger_error_fail_closed``."""


def decide(
    policy: BudgetPolicy, decision: BudgetDecision | None, error: Exception | None
) -> Verdict:
    """Return the verdict for a budget check that answered ``decision``.

    Or raised ``error``.
    """
    if (
        error is not None
        or decision is None
        or decision.reason == LEDGER_UNAVAILABLE_REASON
    ):
        if policy.ledger_error_action() is OnLedgerError.ALLOW:
            return Verdict(
                serve=True, would_exceed=False, reason="ledger_error_fail_open"
            )
        return Verdict(serve=False, would_exceed=False, reason="ledger_error_fail_closed")
    if decision.allowed:
        return Verdict(serve=True, would_exceed=False, reason=decision.reason)
    level = policy.enforcement
    if policy.per_budget and decision.enforcement is not None:
        level = decision.enforcement
    return Verdict(
        serve=level is Enforcement.SHADOW, would_exceed=True, reason=decision.reason
    )
