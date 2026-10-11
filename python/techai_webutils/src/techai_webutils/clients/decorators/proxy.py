"""__getattr__ proxy decorators for cross-cutting concerns.

Proxies use __getattr__ to transparently forward method calls while
adding logging, tracing, retry, or circuit breaker behavior.

**Async-first:** these proxies are async-first — an ``async def`` method is wrapped by an
``async`` wrapper that applies the cross-cutting concern *inside* the ``await`` (so the circuit
breaker/retry see the real failure and the log/span bracket the actual execution). Sync methods
are the derived case, handled by the plain wrapper. The wrapper variant is chosen per method via
``inspect.iscoroutinefunction``.

Usage:
    service = ActualService()
    decorated = LoggingProxy(TracingProxy(service, "svc"), "svc")
    decorated.some_method(args)  # Logs + traces + calls actual method
    await decorated.some_async_method(args)  # awaits inside the log + span scope
"""

from __future__ import annotations

import asyncio
import functools
import inspect
import time
from dataclasses import dataclass
from typing import TYPE_CHECKING

from opentelemetry import trace

from techai_webutils.clients.decorators.metrics_proxy import MetricsProxy
from techai_webutils.core.errors import AppRuntimeError, AppValueError
from techai_webutils.core.errors.errors import AppError
from techai_webutils.foundation.logger.logger import get_logger
from techai_webutils.foundation.resilience.async_retry import retry_after_s
from techai_webutils.foundation.resilience.bulkhead import SemaphoreBulkhead
from techai_webutils.foundation.resilience.timeout import with_timeout

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.bulkhead import Bulkhead
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.hedger import Hedger
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter


class LoggingProxy:
    """Log each wrapped call as a STRUCTURED event (the Python client-seam analog of the Go decorators).

    Emits ``client call`` (entry), ``client call complete`` (with ``duration_ms``), and
    ``client call failed`` at DEBUG through the shared structlog logger, so every line is JSON on
    the canonical schema. Because the ``TracingProxy`` sits OUTSIDE this proxy in the client stack
    (``… → Tracing → Logging``), the span is active when these fire, so the logger's
    ``_add_trace_context`` processor stamps the active ``trace_id``/``span_id`` onto each line —
    a single request is followable across services and each stage's latency (``duration_ms``) is
    legible. This is an inner seam: it logs at DEBUG only (visible in dev, where ``level=debug``;
    suppressed in staging/prod, where only the outermost transport/recovery seam logs at
    INFO/ERROR — ARCHITECTURE.md#decorator-order).
    """

    def __init__(self, wrapped: object, logger_name: str) -> None:
        """Wrap ``wrapped``, logging its calls as structured events via the structlog ``logger_name``."""
        self._wrapped = wrapped
        # Exemption from "inject the logger" (ARCHITECTURE.md#dependency-injection): unlike Go's injected
        # ``interfaces.Logger``, the Python foundation's logging is structlog configured ONCE at the
        # process root (``configure_logging``); every seam resolves a bound logger by name via
        # ``get_logger``. The swap point here is the global structlog config, not a per-instance
        # logger — so resolving by ``logger_name`` is the idiomatic, consistent seam, and threading an
        # instance through ``new_client_stack_from_config`` would diverge from the rest of the package.
        self._logger = get_logger(logger_name)

    def __getattr__(self, name: str) -> object:
        """Intercept attribute access and wrap callable attributes with structured logging.

        Non-callable attributes are returned as-is. Callable attributes are wrapped to log
        entry, completion (with ``duration_ms``), and failure — all at debug level (this is an
        inner seam; only the outermost seam logs Error in staging/prod).
        """
        attr = getattr(self._wrapped, name)
        if not callable(attr):
            return attr

        client = type(self._wrapped).__name__

        if inspect.iscoroutinefunction(attr):

            @functools.wraps(attr)
            async def awrapper(*args: object, **kwargs: object) -> object:
                """Await the wrapped coroutine, logging entry, completion (timed), and failure."""
                self._logger.debug("client call", client=client, method=name)
                start = time.perf_counter()
                try:
                    result = await attr(*args, **kwargs)
                except Exception:
                    # Inner-seam failure logs at Debug (suppressed in staging/prod where
                    # level=info); only the outermost recovery/transport seam logs Error
                    # there (ARCHITECTURE.md#decorator-order; the same rule holds for every stack).
                    self._logger.debug(
                        "client call failed",
                        client=client,
                        method=name,
                        duration_ms=round((time.perf_counter() - start) * 1000, 2),
                        exc_info=True,
                    )
                    raise
                self._logger.debug(
                    "client call complete",
                    client=client,
                    method=name,
                    duration_ms=round((time.perf_counter() - start) * 1000, 2),
                )
                return result

            return awrapper

        @functools.wraps(attr)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped method, logging entry, completion (timed), and failure."""
            self._logger.debug("client call", client=client, method=name)
            start = time.perf_counter()
            try:
                result = attr(*args, **kwargs)
            except Exception:
                self._logger.debug(
                    "client call failed",
                    client=client,
                    method=name,
                    duration_ms=round((time.perf_counter() - start) * 1000, 2),
                    exc_info=True,
                )
                raise
            self._logger.debug(
                "client call complete",
                client=client,
                method=name,
                duration_ms=round((time.perf_counter() - start) * 1000, 2),
            )
            return result

        return wrapper


class TracingProxy:
    """Proxy that creates OTel spans for method calls."""

    def __init__(self, wrapped: object, service_name: str) -> None:
        """Wrap ``wrapped`` and trace its calls under an OTel tracer named ``service_name``."""
        self._wrapped = wrapped
        self._tracer = trace.get_tracer(service_name)

    def __getattr__(self, name: str) -> object:
        """Intercept attribute access and wrap callable attributes with tracing.

        Non-callable attributes are returned as-is. Callable attributes are
        wrapped to execute within an OpenTelemetry span named
        ``<ClassName>.<method>``.
        """
        attr = getattr(self._wrapped, name)
        if not callable(attr):
            return attr

        span_name = f"{type(self._wrapped).__name__}.{name}"

        if inspect.iscoroutinefunction(attr):

            @functools.wraps(attr)
            async def awrapper(*args: object, **kwargs: object) -> object:
                """Await the wrapped coroutine inside an OpenTelemetry span."""
                with self._tracer.start_as_current_span(span_name):
                    return await attr(*args, **kwargs)

            return awrapper

        @functools.wraps(attr)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped method inside an OpenTelemetry span."""
            with self._tracer.start_as_current_span(span_name):
                return attr(*args, **kwargs)

        return wrapper


class RetryProxy:
    """Proxy that retries failed method calls on transient errors."""

    def __init__(
        self,
        wrapped: object,
        max_attempts: int = 3,
        *,
        max_pushback_s: float = 10.0,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        """Wrap ``wrapped``, retrying transient ``AppError``s up to ``max_attempts`` times (minimum 1).

        Retries are IMMEDIATE (no backoff) — the call is re-invoked in a tight loop, which suits fast,
        idempotent calls — except after an async failure carrying a server pushback
        (``details["retry_after_ms"]``, set by the gRPC boundary): the next attempt then waits that
        long, capped at ``max_pushback_s`` seconds, through ``sleep``. A sync method never waits. For
        exponential backoff + jitter use ``retry_transient_async`` instead; do not stack both around
        the same target (their attempt counts multiply).
        """
        if max_attempts < 1:
            msg = f"max_attempts must be >= 1, got {max_attempts}"
            raise AppValueError(msg)
        self._wrapped = wrapped
        self._max_attempts = max_attempts
        self._max_pushback_s = max_pushback_s
        self._sleep = sleep

    def __getattr__(self, name: str) -> object:
        """Intercept attribute access and wrap callable attributes with retry logic.

        Non-callable attributes are returned as-is. Callable attributes are
        wrapped to retry up to ``max_attempts`` times when a transient
        ``AppError`` is raised. Permanent errors propagate immediately.
        """
        attr = getattr(self._wrapped, name)
        if not callable(attr):
            return attr

        if inspect.iscoroutinefunction(attr):

            @functools.wraps(attr)
            async def awrapper(*args: object, **kwargs: object) -> object:
                """Await the wrapped coroutine, retrying transient AppError failures."""
                last_err: BaseException | None = None
                for attempt in range(self._max_attempts):
                    try:
                        return await attr(*args, **kwargs)
                    except AppError as e:
                        if not e.is_transient:
                            raise
                        last_err = e
                        pushback = retry_after_s(e)
                        if pushback is not None and attempt + 1 < self._max_attempts:
                            await self._sleep(min(pushback, self._max_pushback_s))
                if last_err is not None:
                    raise last_err
                msg = "unreachable"
                raise AppRuntimeError(msg)  # pragma: no cover

            return awrapper

        @functools.wraps(attr)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped method, retrying transient AppError failures."""
            last_err: BaseException | None = None
            for _attempt in range(self._max_attempts):
                try:
                    return attr(*args, **kwargs)
                except AppError as e:
                    if not e.is_transient:
                        raise
                    last_err = e
                except Exception:
                    raise
            if last_err is not None:
                raise last_err
            msg = "unreachable"
            raise AppRuntimeError(msg)  # pragma: no cover

        return wrapper


class CircuitBreakerProxy:
    """Proxy that wraps method calls with a circuit breaker."""

    def __init__(self, wrapped: object, circuit_breaker: CircuitBreakerInterface) -> None:
        """Wrap ``wrapped``, gating each call through ``circuit_breaker`` (fails fast when open)."""
        self._wrapped = wrapped
        self._cb = circuit_breaker

    def __getattr__(self, name: str) -> object:
        """Intercept attribute access and wrap callable attributes with circuit breaker protection.

        Non-callable attributes are returned as-is. Callable attributes are
        wrapped so each call executes within the circuit breaker context
        manager. If the circuit is open, ``CircuitOpenError`` is raised
        without invoking the underlying method.
        """
        attr = getattr(self._wrapped, name)
        if not callable(attr):
            return attr

        if inspect.iscoroutinefunction(attr):

            @functools.wraps(attr)
            async def awrapper(*args: object, **kwargs: object) -> object:
                """Await the wrapped coroutine within the circuit breaker context."""
                with self._cb:
                    return await attr(*args, **kwargs)

            return awrapper

        @functools.wraps(attr)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped method within the circuit breaker context."""
            with self._cb:
                return attr(*args, **kwargs)

        return wrapper


class BulkheadProxy:
    """Proxy that bounds concurrent async calls through a Bulkhead.

    Only async methods are gated (the Bulkhead is async); sync attributes pass
    through unchanged.
    """

    def __init__(self, wrapped: object, bulkhead: Bulkhead) -> None:
        """Wrap ``wrapped``, running each async call within ``bulkhead``'s slot limit."""
        self._wrapped = wrapped
        self._bulkhead = bulkhead

    def __getattr__(self, name: str) -> object:
        """Wrap async callables so each runs within the bulkhead's concurrency limit."""
        attr = getattr(self._wrapped, name)
        if not callable(attr) or not inspect.iscoroutinefunction(attr):
            return attr

        @functools.wraps(attr)
        async def awrapper(*args: object, **kwargs: object) -> object:
            """Await the wrapped coroutine within the bulkhead's slot limit."""
            return await self._bulkhead.execute(lambda: attr(*args, **kwargs))

        return awrapper


class TimeoutProxy:
    """Proxy that bounds each async call with a timeout.

    Only async methods are timed (the timeout wraps an awaitable); sync attributes
    pass through unchanged.
    """

    def __init__(self, wrapped: object, timeout_seconds: float) -> None:
        """Wrap ``wrapped``, failing any async call that exceeds ``timeout_seconds``."""
        self._wrapped = wrapped
        self._timeout_seconds = timeout_seconds

    def __getattr__(self, name: str) -> object:
        """Wrap async callables so each is bounded by the configured timeout."""
        attr = getattr(self._wrapped, name)
        if not callable(attr) or not inspect.iscoroutinefunction(attr):
            return attr

        @functools.wraps(attr)
        async def awrapper(*args: object, **kwargs: object) -> object:
            """Await the wrapped coroutine under the configured timeout."""
            return await with_timeout(attr(*args, **kwargs), self._timeout_seconds)

        return awrapper


class RateLimitProxy:
    """Proxy that bounds the RATE of async calls through a RateLimiter (token bucket).

    Only async methods are gated (the limiter's ``wait`` is a coroutine); sync attributes
    pass through unchanged. Each call awaits a token before proceeding, so a burst is
    smoothed to the configured rate — the RateLimit layer of the Job/Controller stacks
    (ARCHITECTURE.md#decorator-order). It is NOT part of the client stack, which has no RateLimit
    layer.
    """

    def __init__(self, wrapped: object, rate_limiter: RateLimiter) -> None:
        """Wrap ``wrapped``, awaiting a token from ``rate_limiter`` before each async call."""
        self._wrapped = wrapped
        self._rate_limiter = rate_limiter

    def __getattr__(self, name: str) -> object:
        """Wrap async callables so each awaits a rate-limit token before running."""
        attr = getattr(self._wrapped, name)
        if not callable(attr) or not inspect.iscoroutinefunction(attr):
            return attr

        @functools.wraps(attr)
        async def awrapper(*args: object, **kwargs: object) -> object:
            """Await a rate-limit token, then await the wrapped coroutine."""
            await self._rate_limiter.wait()
            return await attr(*args, **kwargs)

        return awrapper


class HedgeProxy:
    """Proxy that hedges async calls through a Hedger to cut tail latency.

    Only async methods are hedged (the Hedger races two coroutines); sync attributes pass
    through unchanged. Apply ONLY to idempotent / read-only targets -- a hedged call fires a
    duplicate request, which must not double a side effect. The Hedger's ``DISABLED`` kind makes
    this a passthrough, so a non-idempotent path selects ``DISABLED`` in config.
    """

    def __init__(self, wrapped: object, hedger: Hedger) -> None:
        """Wrap ``wrapped``, racing each async call through ``hedger``."""
        self._wrapped = wrapped
        self._hedger = hedger

    def __getattr__(self, name: str) -> object:
        """Wrap async callables so each is hedged; sync attributes pass through unchanged."""
        attr = getattr(self._wrapped, name)
        if not callable(attr) or not inspect.iscoroutinefunction(attr):
            return attr

        @functools.wraps(attr)
        async def awrapper(*args: object, **kwargs: object) -> object:
            """Race the wrapped coroutine through the hedger, returning the first responder."""
            return await self._hedger.hedge(lambda: attr(*args, **kwargs))

        return awrapper


@dataclass(frozen=True)
class ClientStackConfig:
    """Per-client resilience tuning for the async client stack.

    The Python analog of the Go clients/decorators Config.
    ``enabled`` gates the whole stack (disabled → the bare client). ``retry_enabled``
    is off by default so the stack never doubles up with an SDK/existing retry
    authority. ``timeout_seconds``/``bulkhead_max_concurrent`` of 0 or None disable
    that layer (so a deliberate long timeout, e.g. Ollama's 300s, is not shortened).
    """

    enabled: bool = True
    """Gates the whole stack; False returns the bare client (backwards-compatible)."""

    timeout_seconds: float | None = 30.0
    """Bounds each async call, in seconds; None/0 disables the timeout layer (keeps a deliberate long
    timeout)."""

    retry_enabled: bool = False
    """Opts into the Retry layer (off by default so it never doubles up with an SDK retry authority)."""

    retry_max_attempts: int = 3
    """Caps RetryProxy attempts when ``retry_enabled`` is True."""

    bulkhead_max_concurrent: int | None = 10
    """Bounds concurrent in-flight calls; None/0 disables the bulkhead."""


def new_client_stack_from_config(
    wrapped: object,
    name: str,
    config: ClientStackConfig,
    *,
    circuit_breaker: CircuitBreakerInterface | None = None,
    metrics: MetricsProvider | None = None,
) -> object:
    """Compose the async client resilience stack around ``wrapped``.

    Applies, outermost → innermost,
    ``Bulkhead → Retry → CircuitBreaker → Timeout → Tracing → Metrics → Logging`` — the Python
    analog of the Go clients/decorators stack. The Tracing layer opens a per-call span, and with an
    injected ``metrics`` provider the Metrics layer (``MetricsProxy``) emits the RED series directly:
    ``client_operations_total{client,method,outcome}``, ``client_errors_total{client,method,code}``
    and ``client_operation_duration_seconds{client,method}``, labelled ``client=name``. Without
    ``metrics`` that layer is skipped. The circuit breaker and the metrics provider are injected
    (both are shared across calls); the other layers are built from ``config``. A disabled config
    returns ``wrapped`` unchanged.
    """
    if not config.enabled:
        return wrapped

    proxied: object = LoggingProxy(wrapped, name)
    if metrics is not None:
        proxied = MetricsProxy(proxied, name, metrics)
    proxied = TracingProxy(proxied, name)
    if config.timeout_seconds:
        proxied = TimeoutProxy(proxied, config.timeout_seconds)
    if circuit_breaker is not None:
        proxied = CircuitBreakerProxy(proxied, circuit_breaker)
    if config.retry_enabled:
        proxied = RetryProxy(proxied, config.retry_max_attempts)
    if config.bulkhead_max_concurrent:
        proxied = BulkheadProxy(
            proxied, SemaphoreBulkhead(config.bulkhead_max_concurrent)
        )
    return proxied
