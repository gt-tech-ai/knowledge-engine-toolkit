"""Type-agnostic decoration of one named operation (mirrors Go ``foundation/decorate``).

A custom repository or service operation (anything beyond the tier's CRUD surface) runs
through ``exec_op(chain, op, fn)``, so it gets the same cross-cutting stack as the CRUD
decorators without a per-op decorator class. Each ``new_*`` builds one middleware;
``chain`` composes them, first argument outermost. The tier op chains
(``repos.op_chain.repo_op_chain``, ``services.op_chain.service_op_chain``) assemble them in
their builder's order (ARCHITECTURE.md#decorator-order).

This module depends only on ``core``, so the repository and service tiers can both use it.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ForbiddenError,
    InternalError,
    UnavailableError,
)
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.core.interfaces.tracer import TracerProvider

type OpThunk = Callable[[], Awaitable[object]]
"""The (inner) operation a middleware runs: the next middleware, or the op body."""


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

    def __init__(self, logger: Logger | None, name: str) -> None:
        """Bind the logger (optional) and the subject name."""
        self._logger = logger
        self._name = name

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op, coding any non-AppError failure."""
        try:
            return await next_()
        except AppError:
            raise
        except Exception as exc:
            if self._logger is not None:
                self._logger.exception(
                    "service error recovered", service=self._name, op=op, error=str(exc)
                )
            raise InternalError(str(exc), cause=exc) from exc


def new_recovery(logger: Logger | None, name: str) -> OpMiddleware:
    """Return the recovery middleware (outermost in the service tier)."""
    return _Recovery(logger, name)


class _Tracing:
    """Runs the op inside a ``<span_prefix>.<op>`` span tagged with the subject."""

    def __init__(
        self, tracer: TracerProvider, name: str, span_prefix: str, subject_attr: str
    ) -> None:
        """Bind the tracer, subject name, span prefix and subject attribute key."""
        self._tracer = tracer
        self._name = name
        self._span_prefix = span_prefix
        self._subject_attr = subject_attr

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op in a span, recording a failure on it."""
        with self._tracer.span(
            f"{self._span_prefix}.{op}", **{self._subject_attr: self._name}
        ) as span:
            try:
                return await next_()
            except Exception as exc:
                span.record_error(exc)
                raise


def new_tracing(
    tracer: TracerProvider, name: str, *, span_prefix: str, subject_attr: str
) -> OpMiddleware:
    """Return the tracing middleware; span names match the tier's CRUD spans."""
    return _Tracing(tracer, name, span_prefix, subject_attr)


class _Metrics:
    """Counts the op and its failures and observes its duration under {subject, op}."""

    def __init__(
        self,
        name: str,
        histogram: MetricHistogram,
        subject_label: str,
        executions: MetricCounter | None,
        errors: MetricCounter | None,
    ) -> None:
        """Bind the instruments and the label key for the subject."""
        self._name = name
        self._histogram = histogram
        self._subject_label = subject_label
        self._executions = executions
        self._errors = errors

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op, recording count, error and duration."""
        labels = {self._subject_label: self._name, "op": op}
        if self._executions is not None:
            self._executions.inc(1.0, **labels)
        start = time.monotonic()
        try:
            return await next_()
        except Exception:
            if self._errors is not None:
                self._errors.inc(1.0, **labels)
            raise
        finally:
            self._histogram.observe(time.monotonic() - start, **labels)


def new_metrics(
    name: str,
    histogram: MetricHistogram,
    *,
    subject_label: str,
    executions: MetricCounter | None = None,
    errors: MetricCounter | None = None,
) -> OpMiddleware:
    """Return the metrics middleware; pass the tier's CRUD instruments to share their series."""
    return _Metrics(name, histogram, subject_label, executions, errors)


class _Logging:
    """Logs ``<msg_prefix>.<op>`` on entry and ``... failed`` on failure, both at Debug."""

    def __init__(
        self, logger: Logger, name: str, subject_field: str, msg_prefix: str
    ) -> None:
        """Bind the logger, subject name, subject field key and message prefix."""
        self._logger = logger
        self._name = name
        self._subject_field = subject_field
        self._msg_prefix = msg_prefix

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op with entry/failure Debug logs (the outermost seam owns Error logs)."""
        fields = {self._subject_field: self._name, "custom_operation": op}
        entry = f"{self._msg_prefix}.{op}"
        self._logger.debug(entry, **fields)
        try:
            return await next_()
        except Exception as exc:
            failed = f"{self._msg_prefix}.{op} failed"
            self._logger.debug(failed, **fields, error=str(exc))
            raise


def new_logging(
    logger: Logger, name: str, *, subject_field: str, msg_prefix: str
) -> OpMiddleware:
    """Return the logging middleware."""
    return _Logging(logger, name, subject_field, msg_prefix)


class _Auth:
    """Authorizes ``<name>.<op>`` before the op; a refusal raises ForbiddenError."""

    def __init__(self, name: str, auth_fn: Callable[[str], Awaitable[bool]]) -> None:
        """Bind the subject name and the authorization callable."""
        self._name = name
        self._auth_fn = auth_fn

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Authorize, then run the op."""
        if not await self._auth_fn(f"{self._name}.{op}"):
            raise ForbiddenError
        return await next_()


def new_auth(name: str, auth_fn: Callable[[str], Awaitable[bool]]) -> OpMiddleware:
    """Return the authorization middleware (same contract as ServiceBuilder.with_authorization)."""
    return _Auth(name, auth_fn)


class _Timeout:
    """Bounds the op; expiry raises AppTimeoutError naming ``<msg_prefix>.<op>``."""

    def __init__(self, seconds: float, msg_prefix: str) -> None:
        """Bind the deadline and the message prefix."""
        self._seconds = seconds
        self._msg_prefix = msg_prefix

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op under the deadline."""
        try:
            return await asyncio.wait_for(next_(), timeout=self._seconds)
        except TimeoutError as exc:
            msg = f"{self._msg_prefix}.{op} timed out after {self._seconds}s"
            raise AppTimeoutError(msg) from exc


def new_timeout(seconds: float, *, msg_prefix: str) -> OpMiddleware:
    """Return the timeout middleware."""
    return _Timeout(seconds, msg_prefix)


class _Retry:
    """Runs the op through the injected Retrier."""

    def __init__(self, retrier: Retrier) -> None:
        """Bind the retrier."""
        self._retrier = retrier

    async def wrap_op(self, op: str, next_: OpThunk) -> object:  # noqa: ARG002 - the retrier owns the policy
        """Run the op with the retrier's policy."""
        return await self._retrier.retry(next_)


def new_retry(retrier: Retrier) -> OpMiddleware:
    """Return the retry middleware."""
    return _Retry(retrier)


class _CircuitBreaker:
    """Runs the op inside the breaker; only an open-breaker rejection becomes UnavailableError."""

    def __init__(self, cb: CircuitBreakerInterface) -> None:
        """Bind the breaker."""
        self._cb = cb

    async def wrap_op(self, op: str, next_: OpThunk) -> object:  # noqa: ARG002 - the breaker is per-subject
        """Run the op through the breaker."""
        try:
            with self._cb:
                return await next_()
        except CircuitOpenError as exc:
            msg = "circuit breaker is open"
            raise UnavailableError(msg) from exc


def new_circuit_breaker(cb: CircuitBreakerInterface) -> OpMiddleware:
    """Return the circuit-breaker middleware."""
    return _CircuitBreaker(cb)
