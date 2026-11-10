"""Async retry decorator for transient errors using tenacity.

The async analog of ``retry.retry_transient`` (which wraps a *sync* callable and cannot
await a coroutine). Retries coroutines that raise transient ``AppError``s (TIMEOUT,
UNAVAILABLE); permanent errors are re-raised without retry. A transient error that carries
a server pushback (``details["retry_after_ms"]``, set by the gRPC boundary) waits at least
that long, capped at ``max_delay``.
"""

from __future__ import annotations

import functools
from collections.abc import Awaitable, Callable
from typing import TypeVar, cast

from tenacity import (
    RetryCallState,
    retry,
    retry_if_exception,
    stop_after_attempt,
)

from techai_webutils.core.errors.errors import AppError
from techai_webutils.foundation.resilience.backoff import exponential_jitter_wait

F = TypeVar("F", bound=Callable[..., Awaitable[object]])

RETRY_AFTER_MS_DETAIL = "retry_after_ms"
"""``AppError.details`` key carrying a server-chosen retry delay in whole milliseconds."""


def retry_after_s(err: BaseException) -> float | None:
    """Return the server's retry delay in seconds from an ``AppError``'s details, or None.

    None when ``err`` is not an ``AppError`` or carries no ``retry_after_ms`` detail, or
    the detail is not a non-negative integer.

    Args:
        err: The error.

    Returns:
        The server's retry delay in seconds from an ``AppError``'s details, or None.

    """
    if not isinstance(err, AppError):
        return None
    raw = err.details.get(RETRY_AFTER_MS_DETAIL)
    if raw is None:
        return None
    try:
        delay_ms = int(raw)
    except ValueError:
        return None
    return delay_ms / 1000 if delay_ms >= 0 else None


def _is_transient(err: BaseException) -> bool:
    """Return True only for ``AppError`` instances whose ``is_transient`` flag is set.

    Args:
        err: The error.

    Returns:
        True only for ``AppError`` instances whose ``is_transient`` flag is set.

    """
    return isinstance(err, AppError) and err.is_transient


def retry_transient_async(
    max_attempts: int = 3,
    base_delay: float = 0.1,
    max_delay: float = 10.0,
    sleep: Callable[[float], Awaitable[None]] | None = None,
) -> Callable[[F], F]:
    """Return a decorator that retries a coroutine on transient ``AppError``s.

    ``max_attempts`` bounds the total attempts (including the first try); ``base_delay``
    and ``max_delay`` (seconds) bound the exponential-with-jitter backoff; a server
    pushback (``retry_after_s``) raises one wait to that delay, still capped at
    ``max_delay``. Permanent errors are re-raised without retry. ``sleep`` (optional)
    overrides the coroutine used to wait between attempts — inject a deterministic
    recorder in tests / callers (e.g. a supervisor retrying a job stop) that need to
    control or record the backoff schedule; the default is tenacity's normal async sleep,
    so the behavior is unchanged when it is omitted.

    Args:
        max_attempts: The most attempts.
        base_delay: The first backoff in seconds.
        max_delay: The longest backoff in seconds.
        sleep: Waits between attempts; ``None`` uses ``asyncio.sleep``.

    Returns:
        A decorator that retries a coroutine on transient ``AppError``s.

    """

    def decorator(func: F) -> F:
        """Attach the tenacity async retry policy to *func*, preserving its metadata.

        Args:
            func: The coroutine function.

        Returns:
            The decorated function.

        """
        retry_cond = retry_if_exception(_is_transient)
        stop_cond = stop_after_attempt(max_attempts)
        backoff = exponential_jitter_wait(initial=base_delay, max_delay=max_delay)

        def wait_pol(state: RetryCallState) -> float:
            """Wait the backoff, or the server's pushback when longer.

            (capped at ``max_delay``).

            The backoff is ``base_delay * 2**(n-1)`` plus up to ``base_delay`` of jitter,
            capped at ``max_delay`` (the shared ``exponential_jitter_wait``).

            Args:
                state: The retry's state.

            Returns:
                The seconds to wait.

            """
            delay = backoff(state)
            err = state.outcome.exception() if state.outcome is not None else None
            pushback = retry_after_s(err) if err is not None else None
            return delay if pushback is None else max(delay, min(pushback, max_delay))

        # Only forward ``sleep`` when provided so the default (tenacity's async sleep) is
        # preserved. The tenacity stub types ``sleep`` as a sync callable, but an async
        # sleep is valid for a coroutine function (AsyncRetrying awaits it) — hence the
        # cast on that one branch.
        retrying = (
            retry(
                retry=retry_cond,
                stop=stop_cond,
                wait=wait_pol,
                reraise=True,
                sleep=cast("Callable[[float], None]", sleep),
            )
            if sleep is not None
            else retry(retry=retry_cond, stop=stop_cond, wait=wait_pol, reraise=True)
        )

        @functools.wraps(func)
        async def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped coroutine under the configured retry policy.

            Args:
                *args: The call's positional arguments.
                **kwargs: The call's keyword arguments.

            Returns:
                What the function returns.

            """
            return await func(*args, **kwargs)

        return cast("F", retrying(wrapper))

    return decorator
