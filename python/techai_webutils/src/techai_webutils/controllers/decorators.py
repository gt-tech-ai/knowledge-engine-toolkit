"""Handler decorators for cross-cutting concerns.

Provides logging, metrics, and recovery decorators, and a HandlerBuilder that
composes them in inside-out order: base → metrics → logging → recovery
(recovery outermost).
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ForbiddenError,
    InternalError,
    UnavailableError,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter

type HandlerFunc = Callable[..., Awaitable[object]]
"""Named generic handler type, matching Go's ``HandlerFunc[Req, Resp]`` in
``go/transport/handler.go``."""


class HandlerBuilder[Req, Resp]:
    """Builder that composes decorators around an async handler function.

    Decorators are applied inside-out:
    base → metrics → logging → authorization → recovery (recovery outermost).

    Args:
        base: The async handler function to decorate.
        name: Name used in decorator log/metric labels.

    """

    def __init__(self, base: Callable[[Req], Awaitable[Resp]], name: str) -> None:
        """Initialize the builder with the base handler and decorator label name."""
        self._base = base
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._executions: MetricCounter | None = None
        self._errors: MetricCounter | None = None
        self._recovery: bool = False
        self._auth_fn: Callable[[], Awaitable[bool]] | None = None
        self._timeout: float | None = None
        self._rate_limiter: RateLimiter | None = None

    def with_logging(self, logger: Logger) -> HandlerBuilder[Req, Resp]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(
        self,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> HandlerBuilder[Req, Resp]:
        """Add a metrics decorator.

        Args:
            histogram: Duration histogram.
            executions: Optional counter for total handler invocations.
            errors: Optional counter for failed handler invocations.

        """
        self._histogram = histogram
        self._executions = executions
        self._errors = errors
        return self

    def with_recovery(self) -> HandlerBuilder[Req, Resp]:
        """Add a recovery decorator that catches exceptions."""
        self._recovery = True
        return self

    def with_authorization(
        self, auth_fn: Callable[[], Awaitable[bool]]
    ) -> HandlerBuilder[Req, Resp]:
        """Add an authorization decorator that checks access before execution."""
        self._auth_fn = auth_fn
        return self

    def with_timeout(self, seconds: float) -> HandlerBuilder[Req, Resp]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_rate_limit(self, limiter: RateLimiter) -> HandlerBuilder[Req, Resp]:
        """Add a rate-limiting decorator."""
        self._rate_limiter = limiter
        return self

    def build(self) -> Callable[[Req], Awaitable[Resp]]:
        """Build the decorated handler.

        Chain: base -> timeout -> rate_limit -> metrics -> logging -> authorization -> recovery.
        """
        h: Callable[[Req], Awaitable[Resp]] = self._base

        if self._timeout is not None:
            h = _wrap_timeout(h, self._timeout)

        if self._rate_limiter is not None:
            h = _wrap_rate_limit(h, self._rate_limiter)

        if self._histogram is not None:
            h = _wrap_metrics(
                h, self._name, self._histogram, self._executions, self._errors
            )

        if self._logger is not None:
            h = _wrap_logging(h, self._name, self._logger)

        if self._auth_fn is not None:
            h = _wrap_authorization(h, self._auth_fn)

        if self._recovery:
            h = _wrap_recovery(h)

        return h


def _wrap_metrics[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
    name: str,
    histogram: MetricHistogram,
    executions: MetricCounter | None = None,
    errors: MetricCounter | None = None,
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with metrics recording (execution count, error count, duration)."""

    async def wrapper(req: Req) -> Resp:
        """Record execution/error counts and duration around the inner handler."""
        if executions is not None:
            executions.inc(name=name)
        start = time.monotonic()
        try:
            return await inner(req)
        except Exception:
            if errors is not None:
                errors.inc(name=name)
            raise
        finally:
            duration = time.monotonic() - start
            histogram.observe(duration, name=name)

    return wrapper


def _wrap_logging[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
    name: str,
    logger: Logger,
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with logging."""

    async def wrapper(req: Req) -> Resp:
        """Log entry and, on failure, the exception and elapsed duration."""
        logger.debug("handler.Execute", handler=name)
        start = time.monotonic()
        try:
            return await inner(req)
        except Exception as exc:
            duration = time.monotonic() - start
            logger.exception(
                "handler.Execute failed",
                handler=name,
                duration=duration,
                error=str(exc),
            )
            raise

    return wrapper


def _wrap_authorization[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
    auth_fn: Callable[[], Awaitable[bool]],
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with authorization checking."""

    async def wrapper(req: Req) -> Resp:
        """Raise ForbiddenError when the auth check fails, else run the handler."""
        if not await auth_fn():
            raise ForbiddenError()
        return await inner(req)

    return wrapper


def _wrap_recovery[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with exception recovery.

    Re-raises AppError as-is; wraps all other exceptions as InternalError.
    Matches Go's ``wrapRecovery`` which catches panics, logs with stack trace,
    and returns ``apperr.Internal(...)``.
    """

    async def wrapper(req: Req) -> Resp:
        """Re-raise AppError unchanged; wrap any other exception as InternalError."""
        try:
            return await inner(req)
        except AppError:
            raise
        except Exception as exc:
            raise InternalError(str(exc), cause=exc) from exc

    return wrapper


def _wrap_timeout[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
    seconds: float,
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with an async timeout."""

    async def wrapper(req: Req) -> Resp:
        """Run the handler under a deadline, raising AppTimeoutError on expiry."""
        try:
            return await asyncio.wait_for(inner(req), timeout=seconds)
        except TimeoutError as e:
            msg = f"handler timed out after {seconds}s"
            raise AppTimeoutError(msg) from e

    return wrapper


def _wrap_rate_limit[Req, Resp](
    inner: Callable[[Req], Awaitable[Resp]],
    limiter: RateLimiter,
) -> Callable[[Req], Awaitable[Resp]]:
    """Wrap a handler with rate limiting."""

    async def wrapper(req: Req) -> Resp:
        """Reject with UnavailableError when the limiter denies the call."""
        if not limiter.allow():
            msg = "rate limit exceeded"
            raise UnavailableError(msg)
        return await inner(req)

    return wrapper
