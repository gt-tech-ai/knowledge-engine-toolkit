"""Retry budget for controlling total retry attempts.

Prevents retry storms by limiting the total number of retries
across all callers within a shared budget.
"""

from __future__ import annotations

import threading


class RetryBudgetExhaustedError(Exception):
    """Raised when the retry budget has been fully consumed."""

    def __init__(self, message: str = "retry budget exhausted") -> None:
        """Initialize with an explanatory message (defaulted for the common case)."""
        super().__init__(message)


class RetryBudget:
    """Tracks and limits total retry attempts.

    Args:
        max_retries: Maximum number of retries allowed across all callers.

    """

    def __init__(self, max_retries: int) -> None:
        """Seed the budget with its full allowance, guarded by a lock.

        Args:
            max_retries: Total retry tokens shared across all callers; also the
                value the budget is restored to by :meth:`reset`.

        """
        self._max_retries = max_retries
        self._remaining = max_retries
        self._lock = threading.Lock()

    def consume(self) -> bool:
        """Attempt to consume one retry token.

        Returns True if a token was consumed, False if budget is exhausted.
        """
        with self._lock:
            if self._remaining > 0:
                self._remaining -= 1
                return True
            return False

    def remaining(self) -> int:
        """Return the number of remaining retry tokens."""
        with self._lock:
            return self._remaining

    def reset(self) -> None:
        """Reset the budget to its original maximum."""
        with self._lock:
            self._remaining = self._max_retries
