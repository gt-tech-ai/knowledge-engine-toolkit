"""``exponential_jitter_wait`` — the retry backoff every engine retrier shares.

Tenacity's exponential-jitter schedule, computed here: ``wait_exponential_jitter`` takes
the start delay as ``initial=``, which tenacity 9.2 deprecates for ``multiplier=``, and
9.1 has no ``multiplier=``, so no single call works warning-free across the range the
engine allows. The jitter is drawn from one ``random.SystemRandom`` per wait policy, which
keeps the weak-PRNG scanners clean without a suppression.
"""

from __future__ import annotations

import random
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Callable

    from tenacity import RetryCallState


def exponential_jitter_wait(
    *,
    initial: float,
    max_delay: float,
    exp_base: float = 2.0,
    jitter: float | None = None,
) -> Callable[[RetryCallState], float]:
    """Return a tenacity wait: ``initial * exp_base**(n-1)`` plus jitter, capped.

    At most ``jitter`` is added.

    Args:
        initial: The first retry's base delay, in seconds.
        max_delay: The longest single wait, in seconds.
        exp_base: The growth factor per attempt.
        jitter: The most random delay added to each wait; ``None`` uses ``initial``.

    Returns:
        A wait callable for tenacity's ``wait=`` (``n`` is the attempt number, from 1).

    """
    spread = initial if jitter is None else jitter
    rng = random.SystemRandom()

    def wait(state: RetryCallState) -> float:
        """Wait the capped exponential plus jitter for ``state``'s attempt.

        Args:
            state: The retry's state.

        Returns:
            The seconds to wait.

        """
        try:
            exponential = initial * exp_base ** (state.attempt_number - 1)
        except OverflowError:
            return max_delay
        return max(0.0, min(exponential + rng.uniform(0, spread), max_delay))

    return wait
