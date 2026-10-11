"""Generic cross-cutting decorator implementations shared by the tiers.

The pipeline and workflow tiers (and any Execute(input) -> output tier) reuse
these bodies. The pipeline and workflow decorator modules were ~74% identical: the same
logging / metrics / timeout / recovery / tracing execute-bodies, re-written per
tier with only the label strings, the (optional) metric counters, and the
tracing support differing. This module holds those bodies once as generic,
label-parameterized base classes; each tier keeps its own public decorator
classes as thin subclasses that bake in the tier label, and its own builder
and sync/async bridge adapters (which are tier-typed). A decorator fix is made
here once instead of once per tier.

The base classes are duck-typed on `execute` and do not inherit the tier's
Pipeline/Workflow ABC; the tier subclass supplies that, so `isinstance` and
static typing still see a Pipeline (or Workflow).
"""

from __future__ import annotations

import asyncio
import concurrent.futures
import time
from typing import TYPE_CHECKING, Any, Protocol, runtime_checkable

from techai_webutils.core.errors.errors import AppError, AppTimeoutError, InternalError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.tracer import TracerProvider


@runtime_checkable
class Unwrappable(Protocol):
    """Exposes the inner unit a decorator wraps.

    The Unwrap seam: a test can walk the
    decorator stack to the underlying handler through unwrap() instead of
    reaching into decorator internals. Every decorator base here is Unwrappable.
    """

    def unwrap(self) -> Any:  # noqa: ANN401 — inner is duck-typed per tier.
        """Return the wrapped inner unit."""
        ...


class _UnwrapMixin:
    """Provides unwrap() returning the wrapped inner; mixed into every base."""

    _inner: Any
    """The wrapped inner unit; bound by the concrete decorator's ``__init__``."""

    def unwrap(self) -> Any:  # noqa: ANN401 — inner is duck-typed per tier.
        """Return the inner unit this decorator wraps (the Unwrap seam)."""
        return self._inner


# ---------------------------------------------------------------------------
# Sync decorator bases
# ---------------------------------------------------------------------------


class LoggingDecoratorBase[In, Out](_UnwrapMixin):
    """Logs execution start (Debug) and failure (exception) around a sync inner.

    msg_prefix is the log-message prefix (e.g. "pipeline"); field_key is the
    structured-log field name carrying the instance name (e.g. "pipeline").
    """

    def __init__(
        self,
        inner: Any,  # noqa: ANN401 — duck-typed on execute; tier subclass narrows.
        name: str,
        logger: Logger,
        *,
        msg_prefix: str,
        field_key: str,
    ) -> None:
        """Store the wrapped inner, its label, the logger, and the tier strings."""
        self._inner = inner
        self._name = name
        self._logger = logger
        # Precompute the log messages (avoids an f-string at each log call).
        self._entry_msg = f"{msg_prefix}.Execute"
        self._fail_msg = f"{msg_prefix}.Execute failed"
        self._field_key = field_key

    def execute(self, input_data: In) -> Out:
        """Execute the inner with entry/failure logging."""
        self._logger.debug(self._entry_msg, **{self._field_key: self._name})
        start = time.monotonic()
        try:
            return self._inner.execute(input_data)
        except Exception as exc:
            duration = time.monotonic() - start
            self._logger.exception(
                self._fail_msg,
                **{self._field_key: self._name},
                duration=duration,
                error=str(exc),
            )
            raise


class MetricsDecoratorBase[In, Out](_UnwrapMixin):
    """Records duration and, when provided, execution/error counts (sync inner).

    The counters are optional so the histogram-only pipeline tier and the
    counter-carrying workflow tier share one body.
    """

    def __init__(
        self,
        inner: Any,  # noqa: ANN401 — duck-typed on execute.
        name: str,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Store the wrapped inner, its label, and the metric instruments."""
        self._inner = inner
        self._name = name
        self._histogram = histogram
        self._executions = executions
        self._errors = errors

    def execute(self, input_data: In) -> Out:
        """Execute the inner, recording count/error/duration metrics."""
        if self._executions is not None:
            self._executions.inc(name=self._name)
        start = time.monotonic()
        try:
            return self._inner.execute(input_data)
        except Exception:
            if self._errors is not None:
                self._errors.inc(name=self._name)
            raise
        finally:
            duration = time.monotonic() - start
            self._histogram.observe(duration, name=self._name)


class TimeoutDecoratorBase[In, Out](_UnwrapMixin):
    """Enforces a per-execution timeout on a sync inner via a thread pool.

    noun names the tier in the timeout error message (e.g. "pipeline").
    """

    def __init__(self, inner: Any, timeout: float, *, noun: str) -> None:  # noqa: ANN401
        """Store the wrapped inner, the timeout in seconds, and the error noun."""
        self._inner = inner
        self._timeout = timeout
        self._noun = noun

    def execute(self, input_data: In) -> Out:
        """Execute the inner with a timeout, raising AppTimeoutError on expiry."""
        with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(self._inner.execute, input_data)
            try:
                return future.result(timeout=self._timeout)
            except concurrent.futures.TimeoutError as e:
                msg = f"{self._noun} timed out after {self._timeout}s"
                raise AppTimeoutError(msg) from e


class RecoveryDecoratorBase[In, Out](_UnwrapMixin):
    """Normalizes a sync inner's exceptions.

    Re-raises AppError as-is and wraps every other exception as InternalError.
    """

    def __init__(self, inner: Any) -> None:  # noqa: ANN401 — duck-typed on execute.
        """Store the wrapped inner whose exceptions are normalized."""
        self._inner = inner

    def execute(self, input_data: In) -> Out:
        """Execute the inner, normalizing unexpected exceptions to InternalError."""
        try:
            return self._inner.execute(input_data)
        except AppError:
            raise
        except Exception as e:
            raise InternalError(str(e), cause=e) from e


# ---------------------------------------------------------------------------
# Async decorator bases
# ---------------------------------------------------------------------------


class AsyncLoggingDecoratorBase[In, Out](_UnwrapMixin):
    """Async analogue of LoggingDecoratorBase."""

    def __init__(
        self,
        inner: Any,  # noqa: ANN401 — duck-typed on execute.
        name: str,
        logger: Logger,
        *,
        msg_prefix: str,
        field_key: str,
    ) -> None:
        """Store the wrapped async inner, its label, the logger, and tier strings."""
        self._inner = inner
        self._name = name
        self._logger = logger
        # Precompute the log messages (avoids an f-string at each log call).
        self._entry_msg = f"{msg_prefix}.Execute"
        self._fail_msg = f"{msg_prefix}.Execute failed"
        self._field_key = field_key

    async def execute(self, input_data: In) -> Out:
        """Execute the inner async with entry/failure logging."""
        self._logger.debug(self._entry_msg, **{self._field_key: self._name})
        start = time.monotonic()
        try:
            return await self._inner.execute(input_data)
        except Exception as exc:
            duration = time.monotonic() - start
            self._logger.exception(
                self._fail_msg,
                **{self._field_key: self._name},
                duration=duration,
                error=str(exc),
            )
            raise


class AsyncMetricsDecoratorBase[In, Out](_UnwrapMixin):
    """Async analogue of MetricsDecoratorBase."""

    def __init__(
        self,
        inner: Any,  # noqa: ANN401 — duck-typed on execute.
        name: str,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Store the wrapped async inner, its label, and the metric instruments."""
        self._inner = inner
        self._name = name
        self._histogram = histogram
        self._executions = executions
        self._errors = errors

    async def execute(self, input_data: In) -> Out:
        """Execute the inner async, recording count/error/duration metrics."""
        if self._executions is not None:
            self._executions.inc(name=self._name)
        start = time.monotonic()
        try:
            return await self._inner.execute(input_data)
        except Exception:
            if self._errors is not None:
                self._errors.inc(name=self._name)
            raise
        finally:
            duration = time.monotonic() - start
            self._histogram.observe(duration, name=self._name)


class AsyncTimeoutDecoratorBase[In, Out](_UnwrapMixin):
    """Async analogue of TimeoutDecoratorBase, via asyncio.wait_for."""

    def __init__(self, inner: Any, timeout: float, *, noun: str) -> None:  # noqa: ANN401
        """Store the wrapped async inner, the timeout in seconds, and error noun."""
        self._inner = inner
        self._timeout = timeout
        self._noun = noun

    async def execute(self, input_data: In) -> Out:
        """Execute the inner async with a timeout, raising AppTimeoutError on expiry."""
        try:
            return await asyncio.wait_for(
                self._inner.execute(input_data), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"{self._noun} timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e


class AsyncRecoveryDecoratorBase[In, Out](_UnwrapMixin):
    """Async analogue of RecoveryDecoratorBase."""

    def __init__(self, inner: Any) -> None:  # noqa: ANN401 — duck-typed on execute.
        """Store the wrapped async inner whose exceptions are normalized."""
        self._inner = inner

    async def execute(self, input_data: In) -> Out:
        """Execute the inner async, normalizing unexpected exceptions."""
        try:
            return await self._inner.execute(input_data)
        except AppError:
            raise
        except Exception as e:
            raise InternalError(str(e), cause=e) from e


class AsyncTracingDecoratorBase[In, Out](_UnwrapMixin):
    """Runs async execution inside a span, recording errors on it.

    span_prefix names the span (span_prefix.<name>.execute); field_key carries
    the instance name.
    """

    def __init__(
        self,
        inner: Any,  # noqa: ANN401 — duck-typed on execute.
        name: str,
        tracer: TracerProvider,
        *,
        span_prefix: str,
        field_key: str,
    ) -> None:
        """Store the wrapped async inner, its span label, the tracer, tier strings."""
        self._inner = inner
        self._name = name
        self._tracer = tracer
        self._span_prefix = span_prefix
        self._field_key = field_key

    async def execute(self, input_data: In) -> Out:
        """Execute the inner async inside a span named for the instance."""
        with self._tracer.span(
            f"{self._span_prefix}.{self._name}.execute",
            **{self._field_key: self._name},
        ) as span:
            try:
                return await self._inner.execute(input_data)
            except Exception as exc:
                span.record_error(exc)
                raise
