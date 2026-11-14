"""Retry decorator for transient errors using tenacity.

Retries operations that raise transient AppErrors (TIMEOUT, UNAVAILABLE).
Permanent errors are not retried.
"""

from __future__ import annotations

import functools
from collections.abc import Callable
from typing import TYPE_CHECKING, TypeVar, cast

from tenacity import (
    Retrying,
    retry_if_exception,
    stop_after_attempt,
)

from techai_webutils.core.errors.errors import AppError
from techai_webutils.foundation.resilience.backoff import exponential_jitter_wait
from techai_webutils.foundation.resilience.observer import UNOBSERVED, retries_done

if TYPE_CHECKING:
    from techai_webutils.foundation.resilience.series import Recorder

F = TypeVar("F", bound=Callable[..., object])


def _is_transient(err: BaseException) -> bool:
    """Check whether an error is transient and therefore retriable.

    Returns ``True`` only for ``AppError`` instances whose
    ``is_transient`` flag is set (e.g., TIMEOUT, UNAVAILABLE).
    All other exception types are treated as non-transient.

    Args:
        err: The error.

    Returns:
        Whether the error is transient.

    """
    if isinstance(err, AppError):
        return err.is_transient
    return False


def retry_transient(
    max_attempts: int = 3,
    base_delay: float = 0.1,
    max_delay: float = 10.0,
    recorder: Recorder | None = None,
) -> Callable[[F], F]:
    """Retry the decorated function on transient errors.

    Each finished call is recorded through ``retries_done``: one retry per attempt
    after the first, and an exhaustion when it still failed after a retry.

    Args:
        max_attempts: Maximum number of attempts (including first try).
        base_delay: Initial delay in seconds between retries.
        max_delay: Maximum delay in seconds between retries.
        recorder: Counts the retries and exhaustion (operation ""); ``None``
            records nothing.

    Returns:
        Decorated function that retries on transient errors.

    """
    counted = recorder or UNOBSERVED.recorder
    retrying = Retrying(
        retry=retry_if_exception(_is_transient),
        stop=stop_after_attempt(max_attempts),
        wait=exponential_jitter_wait(initial=base_delay, max_delay=max_delay),
        reraise=True,
    )

    def decorator(func: F) -> F:
        """Attach the tenacity retry policy to *func*, preserving its metadata.

        Args:
            func: The function.

        Returns:
            The decorated function.

        """

        @functools.wraps(func)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped function under the configured retry policy.

            Args:
                *args: The call's positional arguments.
                **kwargs: The call's keyword arguments.

            Returns:
                What the function returns.

            """
            attempts = 0

            def attempt() -> object:
                """Run the function once, counting the attempt.

                Returns:
                    What the function returns.

                """
                nonlocal attempts
                attempts += 1
                return func(*args, **kwargs)

            try:
                result: object = retrying.copy()(attempt)
            except Exception as exc:
                _ = retries_done(counted, "", attempts, exc)
                raise
            _ = retries_done(counted, "", attempts, None)
            return result

        return cast("F", wrapper)

    return decorator
