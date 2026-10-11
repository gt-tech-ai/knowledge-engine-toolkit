"""Fluent builder for single-operation (OpService) domain services.

The Python analog of Go's ``go/services/builder.go``. Dependency injection happens BEFORE
``build`` (the domain ``run`` closes over the service's collaborators); the builder only adds the
cross-cutting decorator stack:

    svc = build(run).named("parse").with_log(logger).with_timeout(30).service()

Decorators are applied inside-out — ``run -> timeout -> logging -> recovery`` — so recovery is ALWAYS
outermost and catches failures from every inner decorator and the operation itself. This is the
operation-oriented counterpart to ``services/decorators/builder.py`` (entity CRUD).
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError, AppTimeoutError, InternalError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.tracer import TracerProvider

type RunFn[A, R] = Callable[[A], Awaitable[R]]
"""A single async service operation: transforms typed args into a typed result."""


class OpBase:
    """Shared base for OpService implementations: a name + optional logger (mirrors Go ``services.Base``).

    Idiosyncratic services embed an ``OpBase`` and read the name/logger via the accessors instead of
    threading them through every call.
    """

    def __init__(self, name: str, logger: Logger | None = None) -> None:
        """Store the service name and (optional) structured logger."""
        self._name = name
        self._log = logger

    @property
    def name(self) -> str:
        """The service name."""
        return self._name

    @property
    def log(self) -> Logger | None:
        """The structured logger (may be None)."""
        return self._log


class _DecoratedOpService[A, R]:
    """The decorated ``OpService[A, R]`` the builder produces (satisfies the OpService protocol)."""

    def __init__(self, run: RunFn[A, R], name: str) -> None:
        """Bind the fully-decorated operation chain and the service name."""
        self._run = run
        self._name = name

    @property
    def name(self) -> str:
        """The service name."""
        return self._name

    async def run(self, args: A) -> R:
        """Execute the fully-decorated operation chain."""
        return await self._run(args)


class OpServiceBuilder[A, R]:
    """Assembles a decorated ``OpService[A, R]`` from a single run function."""

    def __init__(self, run: RunFn[A, R]) -> None:
        """Start a builder around ``run`` with no decorators selected yet."""
        self._run = run
        self._name = ""
        self._log: Logger | None = None
        self._metrics: MetricsProvider | None = None
        self._tracer: TracerProvider | None = None
        self._timeout: float | None = None

    def named(self, name: str) -> OpServiceBuilder[A, R]:
        """Set the service name used in log fields and decorator actions."""
        self._name = name
        return self

    def with_log(self, logger: Logger) -> OpServiceBuilder[A, R]:
        """Add a logging decorator (operation entry at debug, failure at error)."""
        self._log = logger
        return self

    def with_metrics(self, metrics: MetricsProvider) -> OpServiceBuilder[A, R]:
        """Add a metrics decorator (RED: operation count by result + duration histogram)."""
        self._metrics = metrics
        return self

    def with_tracing(self, tracer: TracerProvider) -> OpServiceBuilder[A, R]:
        """Add a tracing decorator (one span per operation, errors recorded on the span)."""
        self._tracer = tracer
        return self

    def with_timeout(self, seconds: float) -> OpServiceBuilder[A, R]:
        """Add a timeout decorator that aborts the operation after ``seconds``."""
        self._timeout = seconds
        return self

    def service(self) -> _DecoratedOpService[A, R]:
        """Build the decorated OpService.

        Chain (inside-out): run -> timeout -> logging -> tracing -> metrics -> recovery. Recovery is
        ALWAYS outermost; metrics wraps tracing so it counts even a recovered failure. Mirrors the Go
        interceptor order (recovery -> metrics -> tracing -> logging -> ... -> handler).
        """
        run = self._run
        if self._timeout is not None:
            run = _with_timeout(self._name, self._timeout, run)
        if self._log is not None:
            run = _with_logging(self._name, self._log, run)
        if self._tracer is not None:
            run = _with_tracing(self._name, self._tracer, run)
        if self._metrics is not None:
            run = _with_metrics(self._name, self._metrics, run)
        run = _with_recovery(self._name, self._log, run)
        return _DecoratedOpService(run, self._name)


def build[A, R](run: RunFn[A, R]) -> OpServiceBuilder[A, R]:
    """Start a fluent builder that assembles a decorated OpService from ``run``."""
    return OpServiceBuilder(run)


def _with_timeout[A, R](name: str, seconds: float, run: RunFn[A, R]) -> RunFn[A, R]:
    """Wrap ``run`` so it aborts with ``AppTimeoutError`` after ``seconds``."""

    async def wrapped(args: A) -> R:
        try:
            return await asyncio.wait_for(run(args), timeout=seconds)
        except TimeoutError as e:
            msg = f"service {name} timed out after {seconds}s"
            raise AppTimeoutError(msg) from e

    return wrapped


def _with_metrics[A, R](
    name: str, metrics: MetricsProvider, run: RunFn[A, R]
) -> RunFn[A, R]:
    """Wrap ``run`` to record RED metrics: an operation counter by result + a duration histogram."""
    counter = metrics.counter(
        "service_operations_total",
        "Service operations by result.",
        ["service", "result"],
    )
    duration = metrics.histogram(
        "service_operation_duration_seconds",
        "Service operation duration in seconds.",
        ["service"],
    )

    async def wrapped(args: A) -> R:
        start = time.monotonic()
        try:
            result = await run(args)
        except Exception:
            counter.inc(service=name, result="error")
            raise
        else:
            counter.inc(service=name, result="success")
            return result
        finally:
            duration.observe(time.monotonic() - start, service=name)

    return wrapped


def _with_tracing[A, R](
    name: str, tracer: TracerProvider, run: RunFn[A, R]
) -> RunFn[A, R]:
    """Wrap ``run`` in a span named ``service.<name>.run``, recording failures on the span."""

    async def wrapped(args: A) -> R:
        with tracer.span(f"service.{name}.run", service=name) as span:
            try:
                return await run(args)
            except Exception as exc:
                span.record_error(exc)
                raise

    return wrapped


def _with_logging[A, R](name: str, logger: Logger, run: RunFn[A, R]) -> RunFn[A, R]:
    """Wrap ``run`` to log operation entry at debug and failure via the logger."""

    async def wrapped(args: A) -> R:
        logger.debug("service.run", service=name)
        try:
            return await run(args)
        except Exception as exc:
            logger.exception("service.run failed", service=name, error=str(exc))
            raise

    return wrapped


def _with_recovery[A, R](
    name: str, logger: Logger | None, run: RunFn[A, R]
) -> RunFn[A, R]:
    """Wrap ``run`` (always outermost) so unexpected exceptions surface as ``InternalError``."""

    async def wrapped(args: A) -> R:
        try:
            return await run(args)
        except AppError:
            raise
        except Exception as exc:
            if logger is not None:
                logger.exception("service.run recovered", service=name)
            msg = f"service {name} failed: {exc}"
            raise InternalError(msg, cause=exc) from exc

    return wrapped
