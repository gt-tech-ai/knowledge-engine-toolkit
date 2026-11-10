"""The decoration layers, one middleware each (mirrors Go ``foundation/decorate``).

A middleware wraps one named operation with a single cross-cutting concern; ``chain``
composes them, first argument outermost, and ``exec_op`` runs an operation through a
chain. Each ``new_*`` builds one layer for the subject ``name`` in the tier ``tier``, so
every tier labels its spans, series and log lines the same way: span ``<name>.<op>`` with
``<tier>.name`` / ``<tier>.operation`` attributes, series ``<tier>_operations_total`` /
``<tier>_errors_total`` / ``<tier>_throttled_total`` /
``<tier>_operation_duration_seconds`` labelled by ``(<tier>, operation)``.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Protocol, cast, final

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ErrorCode,
    ForbiddenError,
    InternalError,
    UnavailableError,
)
from techai_webutils.foundation.errors import OnFailure
from techai_webutils.foundation.resilience.bulkhead import BulkheadFullError
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.foundation.resilience.classify import is_throttled

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
        """Run ``next_`` under this concern for the operation named ``op``.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What ``next_`` returns.

        """
        ...


@final
class _Chain:
    """Middlewares composed outermost-first."""

    def __init__(self, middlewares: tuple[OpMiddleware, ...]) -> None:
        """Store the middlewares, outermost first.

        Args:
            middlewares: The middlewares, outermost first.

        """
        self._middlewares = middlewares

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run ``next_`` inside every middleware, the first one outermost.

        Args:
            op: The operation.
            next_: Runs the operation.

        Returns:
            What the operation returns.

        """
        run = next_
        for mw in reversed(self._middlewares):
            run = _bind(mw, op, run)
        return await run()


def _bind(mw: OpMiddleware, op: str, next_: OpThunk) -> OpThunk:
    """Return a thunk that runs ``next_`` under ``mw``.

    Args:
        mw: The middleware.
        op: The operation.
        next_: The inner thunk.

    Returns:
        A thunk that runs ``next_`` under ``mw``.

    """

    async def run() -> object:
        return await mw.wrap_op(op, next_)

    return run


def chain(*middlewares: OpMiddleware) -> OpMiddleware:
    """Compose middlewares into one; the first argument is outermost.

    Args:
        *middlewares: The middlewares, outermost first.

    Returns:
        The composed middleware.

    """
    return _Chain(middlewares)


async def exec_op[R](mw: OpMiddleware, op: str, fn: Callable[[], Awaitable[R]]) -> R:
    """Run ``fn`` as the operation ``op`` under ``mw`` and return its result unchanged.

    Args:
        mw: The middleware.
        op: The operation.
        fn: The call.

    Returns:
        What ``fn`` returns.

    """
    return cast("R", await mw.wrap_op(op, fn))


@final
class _Recovery:
    """Codes an unexpected exception as InternalError; an AppError passes through."""

    def __init__(self, logger: Logger | None, tier: str, name: str) -> None:
        """Bind the logger (optional), the tier and the subject name.

        Args:
            logger: Logs a panic; ``None`` logs nothing.
            tier: The tier's name.
            name: The component's dotted name.

        """
        self._logger = logger
        self._tier = tier
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op, coding any non-AppError failure.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppError: A failed run, without recovery.
            InternalError: A non-``AppError`` failure, coded.

        """
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
    """Return the recovery layer.

    An unexpected exception becomes INTERNAL, logged when a logger is given.

    Args:
        logger: Logs a panic; ``None`` logs nothing.
        tier: The tier's name.
        name: The subject's dotted name.

    Returns:
        The recovery layer: an unexpected exception becomes INTERNAL.

    """
    return _Recovery(logger, tier, name)


@final
class _Tracing:
    """Runs the op inside a ``<name>.<op>`` span of the tier's kind."""

    def __init__(
        self, tracer: TracerProvider, tier: str, name: str, kind: SpanKind
    ) -> None:
        """Bind the tracer, tier, subject name and span kind.

        Args:
            tracer: Opens the spans.
            tier: The tier's name.
            name: The subject's dotted name.
            kind: The tier's span kind.

        """
        self._tracer = tracer
        self._tier = tier
        self._name = name
        self._kind = kind

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op in a span, recording a failure on it.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        """
        attrs = {f"{self._tier}.name": self._name, f"{self._tier}.operation": op}
        span_name = f"{self._name}.{op}"
        with (
            self._tracer.span(span_name, kind=self._kind, **attrs) as span,
            OnFailure(span.record_error),
        ):
            return await next_()


def new_tracing(
    tracer: TracerProvider, tier: str, name: str, kind: SpanKind
) -> OpMiddleware:
    """Return the tracing layer: a ``<name>.<op>`` span of ``kind`` per operation.

    Args:
        tracer: Opens the spans.
        tier: The tier's name.
        name: The subject's dotted name.
        kind: The tier's span kind.

    Returns:
        The tracing layer: a ``<name>.<op>`` span of ``kind`` per operation.

    """
    return _Tracing(tracer, tier, name, kind)


@final
class _Metrics:
    """Counts the op, its failures and its throttles, and times it.

    Each series is labelled ``(<tier>, operation)``.

    A throttle (``is_throttled``: S3 ``SlowDown``, an AWS throttle code, gRPC
    ``RESOURCE_EXHAUSTED``, HTTP 429) counts as an error and again under
    ``<tier>_throttled_total``, so a quota alert reads the provider's push-back apart from
    every other failure. Below the retry layer each attempt counts.
    """

    def __init__(self, metrics: MetricsProvider, tier: str, name: str) -> None:
        """Create (or retrieve) the tier's instruments.

        Args:
            metrics: Where the instruments are created.
            tier: The tier's name.
            name: The subject's dotted name.

        """
        labels = [tier, "operation"]
        self._ops = metrics.counter(
            f"{tier}_operations_total", f"Total {tier} operations", labels
        )
        self._errs = metrics.counter(
            f"{tier}_errors_total", f"Total {tier} operation errors", labels
        )
        self._throttled = metrics.counter(
            f"{tier}_throttled_total",
            f"Total {tier} operations refused by the provider for rate or quota",
            labels,
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
        """Run the op, recording count, error and duration.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        """
        labels = {self._tier: self._name, "operation": op}
        start = time.monotonic()

        def failed(exc: Exception) -> None:
            """Count the failure, and the throttle when it is one.

            Args:
                exc: The operation's failure.

            """
            self._errs.inc(1.0, **labels)
            if is_throttled(exc):
                self._throttled.inc(1.0, **labels)

        try:
            with OnFailure(failed):
                return await next_()
        finally:
            self._ops.inc(1.0, **labels)
            self._dur.observe(time.monotonic() - start, **labels)


def new_metrics(metrics: MetricsProvider, tier: str, name: str) -> OpMiddleware:
    """Return the metrics layer: the tier's RED series for the subject ``name``.

    Args:
        metrics: Where the instruments are created.
        tier: The tier's name.
        name: The subject's dotted name.

    Returns:
        The metrics layer: the tier's RED series for the subject ``name``.

    """
    return _Metrics(metrics, tier, name)


@final
class _Logging:
    """Logs ``<tier>.<op>`` on entry and ``... failed`` on failure.

    Both are at Debug in an inner tier, so the edge logs the error once. An edge tier
    (``Tier.edge``) logs a failure at Warning when it is a client fault (an HTTP status
    below
    500) and at Error otherwise.
    """

    def __init__(
        self, logger: Logger, tier: str, name: str, *, edge: bool = False
    ) -> None:
        """Bind the logger, tier and subject name, and whether the tier is the edge.

        Args:
            logger: Where the lines go.
            tier: The tier's name.
            name: The subject's dotted name.
            edge: Whether the tier is the edge, which logs a failure once at Warning or
                Error.

        """
        self._logger = logger
        self._tier = tier
        self._name = name
        self._edge = edge

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op with an entry log and a failure log at the tier's level.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        """
        fields: dict[str, object] = {self._tier: self._name, "operation": op}
        entry = f"{self._tier}.{op}"
        self._logger.debug(entry, **fields)

        def failed(exc: Exception) -> None:
            """Log the failure at the tier's level.

            Args:
                exc: The operation's failure.

            """
            self._log_failure(f"{entry} failed", fields, exc)

        with OnFailure(failed):
            return await next_()

    def _log_failure(self, msg: str, fields: dict[str, object], exc: Exception) -> None:
        """Log a failure at Debug, or as the edge at Warning (client fault) or Error.

        Args:
            msg: The line.
            fields: Its fields.
            exc: The failure.

        """
        if not self._edge:
            self._logger.debug(msg, **fields, error=str(exc))
        elif isinstance(exc, AppError) and exc.http_status < _SERVER_ERROR:
            self._logger.warning(msg, **fields, error=str(exc))
        else:
            self._logger.error(msg, **fields, error=str(exc))


_SERVER_ERROR = 500
"""The lowest HTTP status of a server fault; an edge logs a failure below it at
Warning."""


def new_logging(
    logger: Logger, tier: str, name: str, *, edge: bool = False
) -> OpMiddleware:
    """Return the logging layer: Debug in an inner tier, Warning or Error at the edge.

    Args:
        logger: Where the lines go.
        tier: The tier's name.
        name: The subject's dotted name.
        edge: Whether the tier is the edge.

    Returns:
        The logging layer: Debug in an inner tier, Warning or Error at the edge.

    """
    return _Logging(logger, tier, name, edge=edge)


@final
class _Auth:
    """Authorizes ``<name>.<op>`` before the op.

    A refusal raises ForbiddenError naming it.

    The action reaches the logs; the controller edge answers a caller only the code's
    client-safe message.
    """

    def __init__(self, name: str, auth: Authorizer) -> None:
        """Bind the subject name and the authorizer.

        Args:
            name: The subject's dotted name.
            auth: Decides each call.

        """
        self._name = name
        self._auth = auth

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Authorize, then run the op.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            ForbiddenError: ``action``: not allowed.

        """
        action = f"{self._name}.{op}"
        if not await self._auth(action):
            msg = f"{action}: not allowed"
            raise ForbiddenError(msg)
        return await next_()


def new_auth(name: str, auth: Authorizer) -> OpMiddleware:
    """Return the auth layer: ``auth`` must allow ``<name>.<op>``.

    Args:
        name: The subject's dotted name.
        auth: Decides each call.

    Returns:
        The auth layer: ``auth`` must allow ``<name>.<op>``.

    """
    return _Auth(name, auth)


@final
class _Timeout:
    """Bounds the op; expiry raises AppTimeoutError."""

    def __init__(self, seconds: float) -> None:
        """Bind the deadline.

        Args:
            seconds: The deadline.

        """
        self._seconds = seconds

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op under the deadline.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppTimeoutError: ``op`` timed out after ``self._seconds``s.

        """
        try:
            return await asyncio.wait_for(next_(), timeout=self._seconds)
        except TimeoutError as exc:
            msg = f"{op} timed out after {self._seconds}s"
            raise AppTimeoutError(msg) from exc


def new_timeout(seconds: float) -> OpMiddleware:
    """Return the timeout layer: each operation gets ``seconds``.

    Under retry, each attempt does.

    Args:
        seconds: The deadline.

    Returns:
        The timeout layer: each operation.

    """
    return _Timeout(seconds)


@final
class _Retry:
    """Runs the op through the injected Retrier."""

    def __init__(self, retrier: Retrier) -> None:
        """Bind the retrier.

        Args:
            retrier: The retry policy.

        """
        self._retrier = retrier

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op with the retrier's policy.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        """
        del op
        return await self._retrier.retry(next_)


def new_retry(retrier: Retrier) -> OpMiddleware:
    """Return the retry layer.

    Args:
        retrier: The retry policy.

    Returns:
        The retry layer.

    """
    return _Retry(retrier)


@final
class _CircuitBreaker:
    """Runs the op inside the breaker.

    An open-breaker rejection becomes UnavailableError.
    """

    def __init__(self, cb: CircuitBreakerInterface, name: str) -> None:
        """Bind the breaker and the subject it guards.

        Args:
            cb: The breaker.
            name: The subject it guards.

        """
        self._cb = cb
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op through the breaker.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            UnavailableError: Circuit breaker ``self._name`` open.

        """
        del op
        try:
            with self._cb:
                return await next_()
        except CircuitOpenError as exc:
            msg = f"circuit breaker {self._name} open"
            raise UnavailableError(msg) from exc


def new_circuit_breaker(cb: CircuitBreakerInterface, name: str) -> OpMiddleware:
    """Return the circuit-breaker layer for the subject ``name``.

    Args:
        cb: The breaker.
        name: The subject it guards.

    Returns:
        The circuit-breaker layer for the subject ``name``.

    """
    return _CircuitBreaker(cb, name)


@final
class _Bulkhead:
    """Bounds the ops in flight: waits for a slot, or sheds when full."""

    def __init__(self, bulkhead: Bulkhead, *, shed: bool) -> None:
        """Bind the bulkhead and whether a full one sheds.

        Args:
            bulkhead: Bounds the concurrent calls.
            shed: Whether a full bulkhead rejects instead of waiting.

        """
        self._bulkhead = bulkhead
        self._shed = shed

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op inside the bulkhead.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppError: ``op``: concurrency limit reached.

        """
        if not self._shed:
            return await self._bulkhead.execute(next_)
        try:
            return await self._bulkhead.try_execute(next_)
        except BulkheadFullError as exc:
            raise AppError(
                ErrorCode.RESOURCE_EXHAUSTED, f"{op}: concurrency limit reached"
            ) from exc


def new_bulkhead(bulkhead: Bulkhead, *, shed: bool) -> OpMiddleware:
    """Return the bulkhead layer.

    With ``shed``, a full bulkhead rejects (RESOURCE_EXHAUSTED).

    Args:
        bulkhead: Bounds the concurrent calls.
        shed: Whether a full bulkhead rejects instead of waiting.

    Returns:
        The bulkhead layer.

    """
    return _Bulkhead(bulkhead, shed=shed)


@final
class _RateLimit:
    """Rejects an op over the limiter's rate."""

    def __init__(self, limiter: RateLimiter) -> None:
        """Bind the limiter.

        Args:
            limiter: The token bucket.

        """
        self._limiter = limiter

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op when the limiter allows it.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppError: ``op``: rate limit exceeded.

        """
        if not self._limiter.allow():
            raise AppError(ErrorCode.RESOURCE_EXHAUSTED, f"{op}: rate limit exceeded")
        return await next_()


def new_rate_limit(limiter: RateLimiter) -> OpMiddleware:
    """Return the rate-limit layer: an op over the rate fails RESOURCE_EXHAUSTED at once.

    Args:
        limiter: The token bucket.

    Returns:
        The rate-limit layer.

    """
    return _RateLimit(limiter)
