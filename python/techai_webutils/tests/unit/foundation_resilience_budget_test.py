"""Tests for RetryBudget."""

from techai_webutils.foundation.resilience.budget import (
    RetryBudget,
    RetryBudgetExhaustedError,
)


class TestRetryBudget:
    """Test suite for RetryBudget retry attempt tracking and enforcement."""

    def test_consume_within_budget(self) -> None:
        """Test that consume returns True when the budget has remaining attempts.

        **Why this test is important:**
          - Retry budgets prevent infinite retry loops in distributed systems
          - Services must be allowed to retry up to the configured limit
          - Under-counting would deny legitimate retries, over-counting would cause retry storms

        **What it tests:**
          - consume() returns True for each attempt up to max_retries
          - All three attempts succeed when max_retries=3
        """
        budget = RetryBudget(max_retries=3)
        assert budget.consume() is True
        assert budget.consume() is True
        assert budget.consume() is True

    def test_consume_exhausted(self) -> None:
        """Test that consume returns False once the budget is exhausted.

        **Why this test is important:**
          - Exhausted budgets must halt retries to prevent cascading failures
          - Allowing retries beyond the budget would negate the protection mechanism
          - Downstream services rely on bounded retry counts for capacity planning

        **What it tests:**
          - First consume() returns True (within budget)
          - Second consume() returns False (budget exhausted with max_retries=1)
        """
        budget = RetryBudget(max_retries=1)
        assert budget.consume() is True
        assert budget.consume() is False

    def test_remaining(self) -> None:
        """Test that remaining accurately reflects the number of retries left.

        **Why this test is important:**
          - Callers use remaining() to decide whether to attempt a retry or fail fast
          - Inaccurate counts would cause premature exhaustion or excess retries
          - Observability systems report remaining budget for capacity monitoring

        **What it tests:**
          - remaining() equals max_retries before any consumption
          - remaining() decrements by 1 after each consume() call
        """
        budget = RetryBudget(max_retries=5)
        assert budget.remaining() == 5
        budget.consume()
        assert budget.remaining() == 4

    def test_reset(self) -> None:
        """Test that reset restores the budget to its original max_retries value.

        **Why this test is important:**
          - Long-lived services need to reset budgets between request cycles
          - Without reset, a single request's retries would exhaust the budget permanently
          - Reset enables reuse of budget instances across multiple operations

        **What it tests:**
          - After consuming 2 of 3 retries, remaining() equals 1
          - After reset(), remaining() returns to 3
        """
        budget = RetryBudget(max_retries=3)
        budget.consume()
        budget.consume()
        assert budget.remaining() == 1
        budget.reset()
        assert budget.remaining() == 3

    def test_zero_budget(self) -> None:
        """Test that a budget of zero immediately denies all retry attempts.

        **Why this test is important:**
          - Zero-budget configuration disables retries entirely for latency-sensitive paths
          - This edge case must not cause division-by-zero or negative counts
          - Operators use zero budget to disable retries without code changes

        **What it tests:**
          - consume() returns False when max_retries=0
          - remaining() equals 0
        """
        budget = RetryBudget(max_retries=0)
        assert budget.consume() is False
        assert budget.remaining() == 0


class TestRetryBudgetExhaustedError:
    """Test suite for RetryBudgetExhaustedError error type."""

    def test_default_message(self) -> None:
        """Test that the default error message indicates budget exhaustion.

        **Why this test is important:**
          - Error messages are logged and displayed to operators during incidents
          - A clear message enables fast diagnosis of retry exhaustion issues
          - The message must distinguish budget exhaustion from other retry failures

        **What it tests:**
          - str(err) contains "retry budget exhausted"
        """
        err = RetryBudgetExhaustedError()
        assert "retry budget exhausted" in str(err)
