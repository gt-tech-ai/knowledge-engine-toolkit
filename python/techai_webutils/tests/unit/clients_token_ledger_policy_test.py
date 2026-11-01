"""Unit tests for the budget enforcement decision table."""

from __future__ import annotations

from decimal import Decimal

import pytest

from techai_webutils.clients.token_ledger.policy import (
    BudgetPolicy,
    Enforcement,
    OnLedgerError,
    Verdict,
    decide,
)
from techai_webutils.core.interfaces.token_ledger import (
    LEDGER_UNAVAILABLE_REASON,
    BudgetDecision,
)

_ALLOWED = BudgetDecision(
    allowed=True, reason="within_budget", remaining_tokens=None, remaining_cost_usd=None
)
_EXHAUSTED = BudgetDecision(
    allowed=False,
    reason="budget_exhausted",
    remaining_tokens=0,
    remaining_cost_usd=Decimal(0),
)
_FAILED_OPEN = BudgetDecision(
    allowed=True,
    reason=LEDGER_UNAVAILABLE_REASON,
    remaining_tokens=None,
    remaining_cost_usd=None,
)
_ERROR = RuntimeError("redis down")


@pytest.mark.parametrize(
    ("enforcement", "decision", "error", "expected"),
    [
        (
            Enforcement.SHADOW,
            _ALLOWED,
            None,
            Verdict(serve=True, would_exceed=False, reason="within_budget"),
        ),
        (
            Enforcement.SHADOW,
            _EXHAUSTED,
            None,
            Verdict(serve=True, would_exceed=True, reason="budget_exhausted"),
        ),
        (
            Enforcement.SHADOW,
            None,
            _ERROR,
            Verdict(serve=True, would_exceed=False, reason="ledger_error_fail_open"),
        ),
        (
            Enforcement.SOFT,
            _ALLOWED,
            None,
            Verdict(serve=True, would_exceed=False, reason="within_budget"),
        ),
        (
            Enforcement.SOFT,
            _EXHAUSTED,
            None,
            Verdict(serve=False, would_exceed=True, reason="budget_exhausted"),
        ),
        (
            Enforcement.SOFT,
            None,
            _ERROR,
            Verdict(serve=True, would_exceed=False, reason="ledger_error_fail_open"),
        ),
        (
            Enforcement.HARD,
            _ALLOWED,
            None,
            Verdict(serve=True, would_exceed=False, reason="within_budget"),
        ),
        (
            Enforcement.HARD,
            _EXHAUSTED,
            None,
            Verdict(serve=False, would_exceed=True, reason="budget_exhausted"),
        ),
        (
            Enforcement.HARD,
            None,
            _ERROR,
            Verdict(serve=False, would_exceed=False, reason="ledger_error_fail_closed"),
        ),
    ],
)
def test_budget_decision_table(
    enforcement: Enforcement,
    decision: BudgetDecision | None,
    error: Exception | None,
    expected: Verdict,
) -> None:
    """Test every enforcement mode against an allowed, an exhausted and a failed budget check.

    **Why this test is important:**
      - The table is the whole rollout story (shadow → soft → hard); one wrong cell either blocks
        tenants during a dual run or lets a hard cap spend for free.

    **What it tests:**
      - shadow never rejects; soft and hard reject an exhausted budget
      - a ledger error serves under shadow and soft and denies under hard
    """
    assert decide(BudgetPolicy(enforcement=enforcement), decision, error) == expected


def test_soft_fails_open_on_ledger_error() -> None:
    """Test that soft enforcement serves when the ledger fails, including a ledger that failed open itself.

    **Why this test is important:**
      - A FinOps outage must not become a product outage under the default policy.

    **What it tests:**
      - a raised error and a ``ledger_unavailable_fail_open`` decision both serve under ``soft``
    """
    soft = BudgetPolicy(enforcement=Enforcement.SOFT)

    assert decide(soft, None, _ERROR).serve
    assert decide(soft, _FAILED_OPEN, None).serve


def test_hard_fails_closed_on_ledger_error() -> None:
    """Test that hard enforcement denies when the ledger can't answer, unless the policy opts out.

    **Why this test is important:**
      - A missing accounting signal must not become unlimited spend under a hard cap, even when
        the backend reports the outage as a fail-open decision rather than raising.

    **What it tests:**
      - a ``ledger_unavailable_fail_open`` decision denies under ``hard``
      - ``on_ledger_error=ALLOW`` makes ``hard`` serve on a ledger error
    """
    hard = BudgetPolicy(enforcement=Enforcement.HARD)
    lenient = BudgetPolicy(
        enforcement=Enforcement.HARD, on_ledger_error=OnLedgerError.ALLOW
    )

    assert decide(hard, _FAILED_OPEN, None) == Verdict(
        serve=False, would_exceed=False, reason="ledger_error_fail_closed"
    )
    assert decide(lenient, None, _ERROR).serve
