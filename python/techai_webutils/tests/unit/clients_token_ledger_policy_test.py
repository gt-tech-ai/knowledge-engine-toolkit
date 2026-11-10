"""Unit tests for the budget enforcement decision table."""

from __future__ import annotations

import dataclasses
from decimal import Decimal

import pytest

from techai_webutils.core.interfaces.token_ledger import (
    LEDGER_UNAVAILABLE_REASON,
    BudgetDecision,
)
from techai_webutils.core.interfaces.token_ledger import Enforcement as CoreEnforcement
from techai_webutils.foundation.tokens.policy import (
    BudgetPolicy,
    Enforcement,
    OnLedgerError,
    Verdict,
    decide,
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
    """Test every enforcement mode against an allowed.

    An exhausted and a failed budget check.

    **Why this test is important:**
      - The table is the whole rollout story (shadow → soft → hard); one wrong cell either
        blocks tenants during a dual run or lets a hard cap spend for free.

    **What it tests:**
      - shadow never rejects; soft and hard reject an exhausted budget
      - a ledger error serves under shadow and soft and denies under hard

    Args:
        enforcement: The policy's level.
        decision: The budget decision.
        error: The ledger's failure, if any.
        expected: The expected verdict.

    """
    assert decide(BudgetPolicy(enforcement=enforcement), decision, error) == expected


def test_soft_fails_open_on_ledger_error() -> None:
    """Test that soft enforcement serves when the ledger fails.

    Including a ledger that failed open itself.

    **Why this test is important:**
      - A FinOps outage must not become a product outage under the default policy.

    **What it tests:**
      - a raised error and a ``ledger_unavailable_fail_open`` decision both serve under
        ``soft``
    """
    soft = BudgetPolicy(enforcement=Enforcement.SOFT)

    assert decide(soft, None, _ERROR).serve
    assert decide(soft, _FAILED_OPEN, None).serve


def test_hard_fails_closed_on_ledger_error() -> None:
    """Test that hard enforcement denies when the ledger can't answer.

    Unless the policy opts out.

    **Why this test is important:**
      - A missing accounting signal must not become unlimited spend under a hard cap, even
        when the backend reports the outage as a fail-open decision rather than raising.

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


def test_enforcement_is_a_core_type_and_decisions_carry_none_by_default() -> None:
    """Test that ``Enforcement`` lives in core and a decision carries no level by default.

    **Why this test is important:**
      - A ledger backend reports a budget's own enforcement on its ``BudgetDecision``, so
        the enum must be importable from core; existing importers of the policy module and
        every backend that sets no level must keep working.

    **What it tests:**
      - ``core.interfaces.token_ledger.Enforcement`` is the policy module's
        ``Enforcement``
      - a ``BudgetDecision`` built without ``enforcement`` has ``None``
    """
    assert CoreEnforcement is Enforcement
    assert _EXHAUSTED.enforcement is None
    assert _with(_EXHAUSTED, Enforcement.HARD).enforcement is Enforcement.HARD


_LEVELS = (None, Enforcement.SHADOW, Enforcement.SOFT, Enforcement.HARD)
"""Every budget-level enforcement a decision can carry."""


@pytest.mark.parametrize("policy_level", list(Enforcement))
@pytest.mark.parametrize("budget_level", _LEVELS)
@pytest.mark.parametrize("per_budget", [False, True])
def test_per_budget_uses_the_budget_level_for_an_exhausted_budget(
    policy_level: Enforcement, budget_level: Enforcement | None, *, per_budget: bool
) -> None:
    """Test which enforcement decides an exhausted budget with and without ``per_budget``.

    **Why this test is important:**
      - An admin's ``hard`` budget must refuse calls even when the deployment default is
        ``shadow``, but only once a deployment opts in; without the flag nothing changes.

    **What it tests:**
      - exhausted: served exactly when the effective level is ``shadow``, where the
        effective level is the budget's when ``per_budget`` is set and the budget has one,
        else the policy's; ``would_exceed`` is always set
      - within budget: always served, whatever the levels

    Args:
        policy_level: The policy's level.
        budget_level: The budget's own level.
        per_budget: Whether each budget sets its level.

    """
    policy = BudgetPolicy(enforcement=policy_level, per_budget=per_budget)
    effective = budget_level if per_budget and budget_level is not None else policy_level

    exhausted = decide(policy, _with(_EXHAUSTED, budget_level), None)
    allowed = decide(policy, _with(_ALLOWED, budget_level), None)

    assert exhausted == Verdict(
        serve=effective is Enforcement.SHADOW,
        would_exceed=True,
        reason="budget_exhausted",
    )
    assert allowed.serve


def test_per_budget_leaves_ledger_errors_to_the_policy() -> None:
    """Test that a ledger error follows the policy's own enforcement under ``per_budget``.

    **Why this test is important:**
      - A failed-open decision carries no trustworthy budget level, so the deployment's
        fail-open/fail-closed choice must still decide it.

    **What it tests:**
      - a ``soft`` policy serves a failed-open decision that claims ``hard``
      - a ``hard`` policy rejects a raised check
    """
    soft = BudgetPolicy(enforcement=Enforcement.SOFT, per_budget=True)
    hard = BudgetPolicy(enforcement=Enforcement.HARD, per_budget=True)

    assert decide(soft, _with(_FAILED_OPEN, Enforcement.HARD), None).serve
    assert not decide(hard, None, _ERROR).serve


def _with(decision: BudgetDecision, level: Enforcement | None) -> BudgetDecision:
    """Return ``decision`` carrying the budget's own ``level``.

    Args:
        decision: The budget decision.
        level: The budget's level; ``None`` leaves it unset.

    Returns:
        ``decision`` carrying the budget's own ``level``.

    """
    return dataclasses.replace(decision, enforcement=level)
