"""``MetricsProxy`` — RED metrics for every call through the Python client stack.

The Python analog of the Go ``clients/decorators`` metrics layer: a ``__getattr__`` proxy that counts
each call (``client_operations_total{client,method,outcome}``, ``outcome`` is ``ok`` or ``error``),
counts failures by error code (``client_errors_total{client,method,code}``, ``UNKNOWN`` for an
exception that is not an ``AppError``), and observes latency
(``client_operation_duration_seconds{client,method}``). The instruments come from an injected
``MetricsProvider``, created once at construction. Emission is best-effort: a failing metrics
backend is logged at warning and never changes the call's result or exception.

A method that returns an async iterator (a streamed call) is measured as the call that opens the
stream: its duration and outcome cover opening it, not consuming it, and an error raised while
iterating is not counted here. The full duration of a streamed generation is
``gen_ai_request_duration_seconds`` from ``AiSpanEnricher``, which reports at exhaustion.
"""

from __future__ import annotations

import functools
import inspect
from time import perf_counter
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.metrics import MetricsProvider

CLIENT_DURATION_BUCKETS: tuple[float, ...] = (
    0.005,
    0.01,
    0.025,
    0.05,
    0.1,
    0.25,
    0.5,
    1.0,
    2.5,
    5.0,
    10.0,
)
"""``client_operation_duration_seconds`` buckets, in seconds (the Go client stack's defaults)."""


class MetricsProxy:
    """Proxy that records rate, errors and duration for each call on the wrapped client."""

    def __init__(
        self, wrapped: object, client_name: str, metrics: MetricsProvider
    ) -> None:
        """Wrap ``wrapped``, labelling its series ``client=client_name`` on instruments from ``metrics``."""
        self._wrapped = wrapped
        self._client = client_name
        # Resolved by name like LoggingProxy: structlog is configured once at the process root.
        self._logger = get_logger("client_metrics")
        self._ops = metrics.counter(
            "client_operations_total",
            "Client operations by outcome.",
            ["client", "method", "outcome"],
        )
        self._errors = metrics.counter(
            "client_errors_total",
            "Client operation errors by error code.",
            ["client", "method", "code"],
        )
        self._duration = metrics.histogram(
            "client_operation_duration_seconds",
            "Client operation duration in seconds.",
            ["client", "method"],
            list(CLIENT_DURATION_BUCKETS),
        )

    def _record(self, method: str, start: float, error: BaseException | None) -> None:
        """Record one finished call; an emission failure is logged at warning, never raised."""
        try:
            self._emit(method, start, error)
        except Exception:
            self._logger.warning(
                "client metrics emit failed",
                client=self._client,
                method=method,
                exc_info=True,
            )

    def _emit(self, method: str, start: float, error: BaseException | None) -> None:
        """Emit one finished call's outcome, error code (on failure) and duration."""
        self._duration.observe(perf_counter() - start, client=self._client, method=method)
        if error is None:
            self._ops.inc(client=self._client, method=method, outcome="ok")
            return
        self._ops.inc(client=self._client, method=method, outcome="error")
        code = error.code if isinstance(error, AppError) else ErrorCode.UNKNOWN
        self._errors.inc(client=self._client, method=method, code=str(code))

    def __getattr__(self, name: str) -> object:
        """Wrap callable attributes so each call is counted and timed; others pass through."""
        attr = getattr(self._wrapped, name)
        if not callable(attr):
            return attr

        if inspect.iscoroutinefunction(attr):

            @functools.wraps(attr)
            async def awrapper(*args: object, **kwargs: object) -> object:
                """Await the wrapped coroutine and record its outcome and duration."""
                start = perf_counter()
                try:
                    result = await attr(*args, **kwargs)
                except Exception as exc:
                    self._record(name, start, exc)
                    raise
                self._record(name, start, None)
                return result

            return awrapper

        @functools.wraps(attr)
        def wrapper(*args: object, **kwargs: object) -> object:
            """Invoke the wrapped method and record its outcome and duration."""
            start = perf_counter()
            try:
                result = attr(*args, **kwargs)
            except Exception as exc:
                self._record(name, start, exc)
                raise
            self._record(name, start, None)
            return result

        return wrapper
