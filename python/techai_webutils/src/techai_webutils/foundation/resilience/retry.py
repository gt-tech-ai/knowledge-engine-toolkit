"""Retry decorator for transient errors using tenacity.

Retries operations that raise transient AppErrors (TIMEOUT, UNAVAILABLE).
Permanent errors are not retried.
"""

from __future__ import annotations

import functools
from collections.abc import Callable
from typing import TypeVar

from tenacity import (
    retry,
    retry_if_exception,
    stop_after_attempt,
    wait_exponential_jitter,
)

from techai_webutils.core.errors.errors import AppError

F = TypeVar("F", bound=Callable[..., object])


def _is_transient(err: BaseException) -> bool:
    """Check whether an error is transient and therefore retriable.

    Returns ``True`` only for ``AppError`` instances whose
    ``is_transient`` flag is set (e.g., TIMEOUT, UNAVAILABLE).
    All other exception types are treated as non-transient.
    """
    if isinstance(err, AppError):
        return err.is_transient
    return False


def retry_transient(
    max_attempts: int = 3,
    base_delay: float = 0.1,
    max_delay: float = 10.0,
) -> Callable[[F], F]:
    """Retry the decorated function on transient errors.

    Args:
        max_attempts: Maximum number of attempts (including first try).
        base_delay: Initial delay in seconds between retries.
        max_delay: Maximum delay in seconds between retries.

    Returns:
        Decorated function that retries on transient errors.

    """

    def decorator(func: F) -> F:
        """Attach the tenacity retry policy to *func*, preserving its metadata."""

        @retry(
            retry=retry_if_exception(_is_transient),
            stop=stop_after_attempt(max_attempts),
            wait=wait_exponential_jitter(
                initial=base_delay, max=max_delay, jitter=base_delay
            ),
            reraise=True,
        )
        @functools.wraps(func)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped function under the configured retry policy."""
            return func(*args, **kwargs)

        return wrapper  # type: ignore[return-value]

    return decorator
