"""The decoration layers, one middleware each (mirrors Go ``foundation/decorate``).

A middleware wraps one named operation with a single cross-cutting concern; ``chain`` composes
them, first argument outermost, and ``exec_op`` runs an operation through a chain. Each
``new_*`` builds one layer for the subject ``name`` in the tier ``tier``, so every tier labels its
spans, series and log lines the same way: span ``<name>.<op>`` with ``<tier>.name`` /
``<tier>.operation`` attributes, series ``<tier>_operations_total`` / ``<tier>_errors_total`` /
``<tier>_operation_duration_seconds`` labelled by ``(<tier>, operation)``.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ErrorCode,
    ForbiddenError,
    InternalError,
    UnavailableError,
)
from techai_webutils.foundation.resilience.bulkhead import BulkheadFullError
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.bulkhead import Bulkhead
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.core.interfaces.tracer import SpanKind, TracerProvider

type OpThunk = Callable[[], Awaitable[object]]
"""The (inner) operation a middleware runs: the next middleware, or the op body."""

type Authorizer = Callable[[str], Awaitable[bool]]
"""Authorizes an action (``<name>.<op>``); False refuses it."""

_DURATION_BUCKETS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
"""The operation-duration histogram buckets, in seconds (Prometheus' defaults)."""


class OpMiddleware(Protocol):
    """Wraps one named operation with a single cross-cutting concern."""

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run ``next_`` under this concern for the operation named ``op``."""
        ...


class _Chain:
    """Middlewares composed outermost-first."""

    def __init__(self, middlewares: tuple[OpMiddleware, ...]) -> None:
        """Store the middlewares, outermost first."""
        self._middlewares = middlewares

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run ``next_`` inside every middleware, the first one outermost."""
        run = next_
        for mw in reversed(self._middlewares):
            run = _bind(mw, op, run)
        return await run()


def _bind(mw: OpMiddleware, op: str, next_: OpThunk) -> OpThunk:
    """Return a thunk that runs ``next_`` under ``mw``."""

    async def run() -> object:
        return await mw.wrap_op(op, next_)

    return run


def chain(*middlewares: OpMiddleware) -> OpMiddleware:
    """Compose middlewares into one; the first argument is outermost."""
    return _Chain(middlewares)


async def exec_op[R](mw: OpMiddleware, op: str, fn: Callable[[], Awaitable[R]]) -> R:
    """Run ``fn`` as the operation ``op`` under ``mw`` and return its result unchanged."""
    return cast("R", await mw.wrap_op(op, fn))


class _Recovery:
    """Codes an unexpected exception as InternalError; an AppError passes through."""

    def __init__(self, logger: Logger | None, tier: str, name: str) -> None:
        """Bind the logger (optional), the tier and the subject name."""
        self._logger = logger
        self._tier = tier
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op, coding any non-AppError failure."""
        try:
            return await next_()
        except AppError:
            raise
        except Exception as exc:
            if self._logger is not None:
                fields: dict[str, object] = {self._tier: self._name, "operation": op}
                msg = f"{self._tier} panic recovered"
                self._logger.exception(msg, **fields)
            raise InternalError(str(exc), cause=exc) from exc


def new_recovery(logger: Logger | None, tier: str, name: str) -> OpMiddleware:
    """Return the recovery layer: an unexpected exception becomes INTERNAL (logged when given)."""
    return _Recovery(logger, tier, name)


class _Tracing:
    """Runs the op inside a ``<name>.<op>`` span of the tier's kind."""

    def __init__(
        self, tracer: TracerProvider, tier: str, name: str, kind: SpanKind
    ) -> None:
        """Bind the tracer, tier, subject name and span kind."""
        self._tracer = tracer
        self._tier = tier
        self._name = name
        self._kind = kind

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op in a span, recording a failure on it."""
        attrs = {f"{self._tier}.name": self._name, f"{self._tier}.operation": op}
        with self._tracer.span(f"{self._name}.{op}", kind=self._kind, **attrs) as span:
            try:
                return await next_()
            except Exception as exc:
                span.record_error(exc)
                raise


def new_tracing(
    tracer: TracerProvider, tier: str, name: str, kind: SpanKind
) -> OpMiddleware:
    """Return the tracing layer: a ``<name>.<op>`` span of ``kind`` per operation."""
    return _Tracing(tracer, tier, name, kind)


class _Metrics:
    """Counts the op and its failures and observes its duration under ``(<tier>, operation)``."""

    def __init__(self, metrics: MetricsProvider, tier: str, name: str) -> None:
        """Create (or retrieve) the tier's instruments."""
        labels = [tier, "operation"]
        self._ops = metrics.counter(
            f"{tier}_operations_total", f"Total {tier} operations", labels
        )
        self._errs = metrics.counter(
            f"{tier}_errors_total", f"Total {tier} operation errors", labels
        )
        self._dur = metrics.histogram(
            f"{tier}_operation_duration_seconds",
            f"{tier} operation duration",
            labels,
            _DURATION_BUCKETS,
        )
        self._tier = tier
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op, recording count, error and duration."""
        labels = {self._tier: self._name, "operation": op}
        start = time.monotonic()
        try:
            return await next_()
        except Exception:
            self._errs.inc(1.0, **labels)
            raise
        finally:
            self._ops.inc(1.0, **labels)
            self._dur.observe(time.monotonic() - start, **labels)


def new_metrics(metrics: MetricsProvider, tier: str, name: str) -> OpMiddleware:
    """Return the metrics layer: the tier's RED series for the subject ``name``."""
    return _Metrics(metrics, tier, name)


class _Logging:
    """Logs ``<tier>.<op>`` on entry and ``... failed`` on failure.

    Both are at Debug in an inner tier, so the edge logs the error once. An edge tier
    (``Tier.edge``) logs a failure at Warning when it is a client fault (an HTTP status below
    500) and at Error otherwise.
    """

    def __init__(
        self, logger: Logger, tier: str, name: str, *, edge: bool = False
    ) -> None:
        """Bind the logger, tier and subject name, and whether the tier is the edge."""
        self._logger = logger
        self._tier = tier
        self._name = name
        self._edge = edge

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op with an entry log and a failure log at the tier's level."""
        fields: dict[str, object] = {self._tier: self._name, "operation": op}
        entry = f"{self._tier}.{op}"
        self._logger.debug(entry, **fields)
        try:
            return await next_()
        except Exception as exc:
            self._log_failure(f"{entry} failed", fields, exc)
            raise

    def _log_failure(self, msg: str, fields: dict[str, object], exc: Exception) -> None:
        """Log a failure at Debug, or as the edge at Warning (client fault) or Error."""
        if not self._edge:
            self._logger.debug(msg, **fields, error=str(exc))
        elif isinstance(exc, AppError) and exc.http_status < _SERVER_ERROR:
            self._logger.warning(msg, **fields, error=str(exc))
        else:
            self._logger.error(msg, **fields, error=str(exc))


_SERVER_ERROR = 500
"""The lowest HTTP status of a server fault; an edge logs a failure below it at Warning."""


def new_logging(
    logger: Logger, tier: str, name: str, *, edge: bool = False
) -> OpMiddleware:
    """Return the logging layer: Debug in an inner tier, Warning or Error at the edge."""
    return _Logging(logger, tier, name, edge=edge)


class _Auth:
    """Authorizes ``<name>.<op>`` before the op; a refusal raises ForbiddenError naming it.

    The action reaches the logs; the controller edge answers a caller only the code's
    client-safe message.
    """

    def __init__(self, name: str, auth: Authorizer) -> None:
        """Bind the subject name and the authorizer."""
        self._name = name
        self._auth = auth

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Authorize, then run the op."""
        action = f"{self._name}.{op}"
        if not await self._auth(action):
            msg = f"{action}: not allowed"
            raise ForbiddenError(msg)
        return await next_()


def new_auth(name: str, auth: Authorizer) -> OpMiddleware:
    """Return the auth layer: ``auth`` must allow ``<name>.<op>``."""
    return _Auth(name, auth)


class _Timeout:
    """Bounds the op; expiry raises AppTimeoutError."""

    def __init__(self, seconds: float) -> None:
        """Bind the deadline."""
        self._seconds = seconds

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op under the deadline."""
        try:
            return await asyncio.wait_for(next_(), timeout=self._seconds)
        except TimeoutError as exc:
            msg = f"{op} timed out after {self._seconds}s"
            raise AppTimeoutError(msg) from exc


def new_timeout(seconds: float) -> OpMiddleware:
    """Return the timeout layer: each operation (each attempt, under retry) gets ``seconds``."""
    return _Timeout(seconds)


class _Retry:
    """Runs the op through the injected Retrier."""

    def __init__(self, retrier: Retrier) -> None:
        """Bind the retrier."""
        self._retrier = retrier

    async def wrap_op(self, op: str, next_: OpThunk) -> object:  # noqa: ARG002 - the retrier owns the policy
        """Run the op with the retrier's policy."""
        return await self._retrier.retry(next_)


def new_retry(retrier: Retrier) -> OpMiddleware:
    """Return the retry layer."""
    return _Retry(retrier)


class _CircuitBreaker:
    """Runs the op inside the breaker; an open-breaker rejection becomes UnavailableError."""

    def __init__(self, cb: CircuitBreakerInterface, name: str) -> None:
        """Bind the breaker and the subject it guards."""
        self._cb = cb
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:  # noqa: ARG002 - the breaker is per-subject
        """Run the op through the breaker."""
        try:
            with self._cb:
                return await next_()
        except CircuitOpenError as exc:
            msg = f"circuit breaker {self._name} open"
            raise UnavailableError(msg) from exc


def new_circuit_breaker(cb: CircuitBreakerInterface, name: str) -> OpMiddleware:
    """Return the circuit-breaker layer for the subject ``name``."""
    return _CircuitBreaker(cb, name)


class _Bulkhead:
    """Bounds the ops in flight: waits for a slot, or sheds when full."""

    def __init__(self, bulkhead: Bulkhead, *, shed: bool) -> None:
        """Bind the bulkhead and whether a full one sheds."""
        self._bulkhead = bulkhead
        self._shed = shed

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op inside the bulkhead."""
        if not self._shed:
            return await self._bulkhead.execute(next_)
        try:
            return await self._bulkhead.try_execute(next_)
        except BulkheadFullError as exc:
            raise AppError(
                ErrorCode.RESOURCE_EXHAUSTED, f"{op}: concurrency limit reached"
            ) from exc


def new_bulkhead(bulkhead: Bulkhead, *, shed: bool) -> OpMiddleware:
    """Return the bulkhead layer; with ``shed`` a full bulkhead rejects (RESOURCE_EXHAUSTED)."""
    return _Bulkhead(bulkhead, shed=shed)


class _RateLimit:
    """Rejects an op over the limiter's rate."""

    def __init__(self, limiter: RateLimiter) -> None:
        """Bind the limiter."""
        self._limiter = limiter

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op when the limiter allows it."""
        if not self._limiter.allow():
            raise AppError(ErrorCode.RESOURCE_EXHAUSTED, f"{op}: rate limit exceeded")
        return await next_()


def new_rate_limit(limiter: RateLimiter) -> OpMiddleware:
    """Return the rate-limit layer: an op over the rate fails RESOURCE_EXHAUSTED at once."""
    return _RateLimit(limiter)
