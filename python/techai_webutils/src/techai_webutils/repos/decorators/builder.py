"""Fluent builder for composing repository decorators.

Mirrors Go's ``repos/repository/decorators/builder.go``.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, TypeVar

from techai_webutils.core.errors.errors import AppTimeoutError, UnavailableError
from techai_webutils.core.interfaces.repository import Repository
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.core.interfaces.tracer import TracerProvider

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class _TimeoutRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Wraps repository operations with an async timeout."""

    def __init__(self, inner: Repository[T, P, ID], timeout: float) -> None:
        """Wrap ``inner`` so every operation is bounded by ``timeout`` seconds."""
        self._inner = inner
        self._timeout = timeout

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity, raising AppTimeoutError if it exceeds the deadline."""
        try:
            return await asyncio.wait_for(
                self._inner.get(entity_id), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"repo.get timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities, raising AppTimeoutError if it exceeds the deadline."""
        try:
            return await asyncio.wait_for(
                self._inner.list(params, page), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"repo.list timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def create(self, entity: T) -> T:
        """Create an entity, raising AppTimeoutError if it exceeds the deadline."""
        try:
            return await asyncio.wait_for(
                self._inner.create(entity), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"repo.create timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity, raising AppTimeoutError if it exceeds the deadline."""
        try:
            return await asyncio.wait_for(
                self._inner.update(entity_id, entity), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"repo.update timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity, raising AppTimeoutError if it exceeds the deadline."""
        try:
            await asyncio.wait_for(self._inner.delete(entity_id), timeout=self._timeout)
        except TimeoutError as e:
            msg = f"repo.delete timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def exists(self, entity_id: ID) -> bool:
        """Report whether an entity exists, raising AppTimeoutError on deadline."""
        try:
            return await asyncio.wait_for(
                self._inner.exists(entity_id), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"repo.exists timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e


class _RetryRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Wraps repository operations with automatic retry via a Retrier."""

    def __init__(self, inner: Repository[T, P, ID], retrier: Retrier) -> None:
        """Wrap ``inner`` so every operation is retried by ``retrier`` on failure."""
        self._inner = inner
        self._retrier = retrier

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository get (closure passed to the retrier)."""
            return await self._inner.get(entity_id)

        return await self._retrier.retry(_op)  # type: ignore[return-value]

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository list (closure passed to the retrier)."""
            return await self._inner.list(params, page)

        return await self._retrier.retry(_op)  # type: ignore[return-value]

    async def create(self, entity: T) -> T:
        """Create an entity, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository create (closure passed to the retrier)."""
            return await self._inner.create(entity)

        return await self._retrier.retry(_op)  # type: ignore[return-value]

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository update (closure passed to the retrier)."""
            return await self._inner.update(entity_id, entity)

        return await self._retrier.retry(_op)  # type: ignore[return-value]

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository delete (closure passed to the retrier)."""
            await self._inner.delete(entity_id)
            return None

        await self._retrier.retry(_op)

    async def exists(self, entity_id: ID) -> bool:
        """Report whether an entity exists, retrying transient failures via the retrier."""

        async def _op() -> object:
            """Run the wrapped repository exists (closure passed to the retrier)."""
            return await self._inner.exists(entity_id)

        return await self._retrier.retry(_op)  # type: ignore[return-value]


class _CircuitBreakerRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Wraps repository operations with a circuit breaker."""

    def __init__(self, inner: Repository[T, P, ID], cb: CircuitBreakerInterface) -> None:
        """Wrap ``inner`` so every operation passes through circuit breaker ``cb``."""
        self._inner = inner
        self._cb = cb

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                return await self._inner.get(entity_id)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                return await self._inner.list(params, page)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    async def create(self, entity: T) -> T:
        """Create an entity through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                return await self._inner.create(entity)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                return await self._inner.update(entity_id, entity)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                await self._inner.delete(entity_id)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    async def exists(self, entity_id: ID) -> bool:
        """Report existence through the breaker, surfacing a trip as UnavailableError."""
        try:
            with self._cb:
                return await self._inner.exists(entity_id)
        except Exception as e:
            self._raise_if_circuit_open(e)
            raise

    @staticmethod
    def _raise_if_circuit_open(exc: Exception) -> None:
        """Translate an open-circuit error into an UnavailableError; pass others through."""
        if isinstance(exc, CircuitOpenError):
            msg = "circuit breaker is open"
            raise UnavailableError(msg) from exc


class _LoggingRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Logs repository operations at debug level on entry, error level on failure.

    Matches Go's ``loggingDecorator`` which logs entry at Debug AND errors at Error
    with duration.
    """

    def __init__(self, inner: Repository[T, P, ID], logger: Logger, name: str) -> None:
        """Wrap ``inner``, tagging log records with repository ``name``."""
        self._inner = inner
        self._logger = logger
        self._name = name

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity, logging entry at debug and any failure at error level."""
        self._logger.debug("repo.get", repo=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            return await self._inner.get(entity_id)
        except Exception as exc:
            self._logger.exception(
                "repo.get failed",
                repo=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities, logging entry at debug and any failure at error level."""
        self._logger.debug("repo.list", repo=self._name)
        start = time.monotonic()
        try:
            return await self._inner.list(params, page)
        except Exception as exc:
            self._logger.exception(
                "repo.list failed",
                repo=self._name,
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def create(self, entity: T) -> T:
        """Create an entity, logging entry at debug and any failure at error level."""
        self._logger.debug("repo.create", repo=self._name)
        start = time.monotonic()
        try:
            return await self._inner.create(entity)
        except Exception as exc:
            self._logger.exception(
                "repo.create failed",
                repo=self._name,
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity, logging entry at debug and any failure at error level."""
        self._logger.debug("repo.update", repo=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            return await self._inner.update(entity_id, entity)
        except Exception as exc:
            self._logger.exception(
                "repo.update failed",
                repo=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity, logging entry at debug and any failure at error level."""
        self._logger.debug("repo.delete", repo=self._name, id=str(entity_id))
        start = time.monotonic()
        try:
            await self._inner.delete(entity_id)
        except Exception as exc:
            self._logger.exception(
                "repo.delete failed",
                repo=self._name,
                id=str(entity_id),
                duration=time.monotonic() - start,
                error=str(exc),
            )
            raise

    async def exists(self, entity_id: ID) -> bool:
        """Report whether an entity exists (delegated unlogged, as in Go's decorator)."""
        return await self._inner.exists(entity_id)


class _MetricsRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Records execution count, error count, and latency for repository operations.

    Matches Go's ``metricsDecorator`` which records 3 instruments:
    executions Counter, errors Counter, duration Histogram.
    """

    def __init__(
        self,
        inner: Repository[T, P, ID],
        histogram: MetricHistogram,
        name: str,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> None:
        """Wrap ``inner``, recording metrics tagged with repository ``name``.

        Args:
            inner: The repository whose operations are instrumented.
            histogram: Duration histogram observed for every operation.
            name: Repository label attached to each metric sample.
            executions: Optional counter incremented once per operation.
            errors: Optional counter incremented when an operation raises.

        """
        self._inner = inner
        self._histogram = histogram
        self._name = name
        self._executions = executions
        self._errors = errors

    async def _observe(self, op: str, coro: object) -> object:
        """Await ``coro`` while recording execution, error, and duration metrics for ``op``."""
        if self._executions is not None:
            self._executions.inc(repo=self._name, op=op)
        start = time.monotonic()
        try:
            return await coro  # type: ignore[misc]
        except Exception:
            if self._errors is not None:
                self._errors.inc(repo=self._name, op=op)
            raise
        finally:
            self._histogram.observe(time.monotonic() - start, repo=self._name, op=op)

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity, recording metrics for the ``get`` operation."""
        return await self._observe("get", self._inner.get(entity_id))  # type: ignore[return-value]

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities, recording metrics for the ``list`` operation."""
        return await self._observe("list", self._inner.list(params, page))  # type: ignore[return-value]

    async def create(self, entity: T) -> T:
        """Create an entity, recording metrics for the ``create`` operation."""
        return await self._observe("create", self._inner.create(entity))  # type: ignore[return-value]

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity, recording metrics for the ``update`` operation."""
        return await self._observe("update", self._inner.update(entity_id, entity))  # type: ignore[return-value]

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity, recording metrics for the ``delete`` operation."""
        await self._observe("delete", self._inner.delete(entity_id))

    async def exists(self, entity_id: ID) -> bool:
        """Report whether an entity exists, recording metrics for the ``exists`` operation."""
        return await self._observe("exists", self._inner.exists(entity_id))  # type: ignore[return-value]


class _TracingRepositoryDecorator[T, P, ID](Repository[T, P, ID]):
    """Creates an OTel span per repository operation.

    Matches Go's ``tracingDecorator`` in ``repos/repository/decorators/builder.go``.
    """

    def __init__(
        self, inner: Repository[T, P, ID], tracer: TracerProvider, name: str
    ) -> None:
        """Wrap ``inner``, naming each span after repository ``name``."""
        self._inner = inner
        self._tracer = tracer
        self._name = name

    async def _traced(self, op: str, coro: object) -> object:
        """Await ``coro`` inside a ``repo.{op}`` span, recording any error on the span."""
        with self._tracer.span(f"repo.{op}", repo=self._name) as span:
            try:
                return await coro  # type: ignore[misc]
            except Exception as exc:
                span.record_error(exc)
                raise

    async def get(self, entity_id: ID) -> T:
        """Fetch one entity within a ``repo.get`` span."""
        return await self._traced("get", self._inner.get(entity_id))  # type: ignore[return-value]

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """List a page of entities within a ``repo.list`` span."""
        return await self._traced("list", self._inner.list(params, page))  # type: ignore[return-value]

    async def create(self, entity: T) -> T:
        """Create an entity within a ``repo.create`` span."""
        return await self._traced("create", self._inner.create(entity))  # type: ignore[return-value]

    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an entity within a ``repo.update`` span."""
        return await self._traced("update", self._inner.update(entity_id, entity))  # type: ignore[return-value]

    async def delete(self, entity_id: ID) -> None:
        """Delete an entity within a ``repo.delete`` span."""
        await self._traced("delete", self._inner.delete(entity_id))

    async def exists(self, entity_id: ID) -> bool:
        """Report whether an entity exists within a ``repo.exists`` span."""
        return await self._traced("exists", self._inner.exists(entity_id))  # type: ignore[return-value]


class RepositoryBuilder[T, P, ID]:
    """Builder that composes decorators around a Repository.

    Example::

        repo = (RepositoryBuilder(base_repo, "users")
            .with_logging(logger)
            .with_metrics(histogram)
            .with_tracing(tracer)
            .build())
    """

    def __init__(self, inner: Repository[T, P, ID], name: str) -> None:
        """Start a builder around base repository ``inner``, labelled ``name``."""
        self._inner = inner
        self._name = name
        self._logger: Logger | None = None
        self._histogram: MetricHistogram | None = None
        self._executions: MetricCounter | None = None
        self._errors: MetricCounter | None = None
        self._tracer: TracerProvider | None = None
        self._timeout: float | None = None
        self._retrier: Retrier | None = None
        self._cb: CircuitBreakerInterface | None = None

    def with_logging(self, logger: Logger) -> RepositoryBuilder[T, P, ID]:
        """Add a logging decorator."""
        self._logger = logger
        return self

    def with_metrics(
        self,
        histogram: MetricHistogram,
        executions: MetricCounter | None = None,
        errors: MetricCounter | None = None,
    ) -> RepositoryBuilder[T, P, ID]:
        """Add a metrics decorator.

        Args:
            histogram: Duration histogram.
            executions: Optional counter for total operations.
            errors: Optional counter for failed operations.

        """
        self._histogram = histogram
        self._executions = executions
        self._errors = errors
        return self

    def with_tracing(self, tracer: TracerProvider) -> RepositoryBuilder[T, P, ID]:
        """Add a tracing decorator."""
        self._tracer = tracer
        return self

    def with_timeout(self, seconds: float) -> RepositoryBuilder[T, P, ID]:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def with_retry(self, retrier: Retrier) -> RepositoryBuilder[T, P, ID]:
        """Add a retry decorator."""
        self._retrier = retrier
        return self

    def with_circuit_breaker(
        self, cb: CircuitBreakerInterface
    ) -> RepositoryBuilder[T, P, ID]:
        """Add a circuit breaker decorator."""
        self._cb = cb
        return self

    def build(self) -> Repository[T, P, ID]:
        """Build the decorated repository.

        Chain: base -> circuit_breaker -> retry -> timeout -> tracing -> metrics -> logging.
        """
        repo: Repository[T, P, ID] = self._inner
        if self._cb is not None:
            repo = _CircuitBreakerRepositoryDecorator(repo, self._cb)
        if self._retrier is not None:
            repo = _RetryRepositoryDecorator(repo, self._retrier)
        if self._timeout is not None:
            repo = _TimeoutRepositoryDecorator(repo, self._timeout)
        if self._tracer is not None:
            repo = _TracingRepositoryDecorator(repo, self._tracer, self._name)
        if self._histogram is not None:
            repo = _MetricsRepositoryDecorator(
                repo,
                self._histogram,
                self._name,
                self._executions,
                self._errors,
            )
        if self._logger is not None:
            repo = _LoggingRepositoryDecorator(repo, self._logger, self._name)
        return repo
