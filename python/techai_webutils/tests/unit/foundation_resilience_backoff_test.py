"""Unit tests for ``exponential_jitter_wait``: the shared retry backoff schedule."""

from __future__ import annotations

from unittest.mock import MagicMock

import pytest
from hypothesis import given
from hypothesis import strategies as st
from tenacity import RetryCallState

from techai_webutils.foundation.resilience.backoff import exponential_jitter_wait


def _state(attempt: int) -> RetryCallState:
    """Return a retry state at ``attempt`` (1-based)."""
    state = MagicMock(spec=RetryCallState)
    state.attempt_number = attempt
    return state


@given(
    attempt=st.integers(min_value=1, max_value=40),
    initial=st.floats(min_value=0.001, max_value=5),
    exp_base=st.floats(min_value=1, max_value=4),
    jitter=st.floats(min_value=0, max_value=2),
    max_delay=st.floats(min_value=0.01, max_value=60),
)
def test_wait_is_the_capped_exponential_plus_jitter(
    attempt: int, initial: float, exp_base: float, jitter: float, max_delay: float
) -> None:
    """Test that every wait lies in the exponential's jitter window.

    Capped at ``max_delay``.

    **Why this test is important:**
      - Every retrier in the engine shares this schedule; a wait above the cap stalls a
        caller, and one below the exponential hammers a struggling dependency.

    **What it tests:**
      - ``initial * exp_base**(n-1) <= wait <= that + jitter``, both bounds capped at
        ``max_delay``
    """
    wait = exponential_jitter_wait(
        initial=initial, max_delay=max_delay, exp_base=exp_base, jitter=jitter
    )

    delay = wait(_state(attempt))

    low = initial * exp_base ** (attempt - 1)
    assert min(low, max_delay) <= delay <= min(low + jitter, max_delay)


def test_wait_saturates_at_the_cap_instead_of_overflowing() -> None:
    """Test that a huge attempt number waits ``max_delay`` rather than raising.

    **Why this test is important:**
      - ``2 ** n`` overflows a float for a long-running retrier; the wait must stay
        bounded.

    **What it tests:**
      - attempt 5000 waits exactly ``max_delay``
    """
    wait = exponential_jitter_wait(initial=1.0, max_delay=7.5, jitter=0.0)

    assert wait(_state(5000)) == pytest.approx(7.5)
