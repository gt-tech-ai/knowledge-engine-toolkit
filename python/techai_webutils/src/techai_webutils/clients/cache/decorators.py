"""Cache decorator builder for cross-cutting concerns.

Wraps a ``Cache`` ABC with logging and metrics decorators in a
fluent builder pattern.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING

from techai_webutils.core.errors.errors import AppTimeoutError
from techai_webutils.core.interfaces.cache import Cache
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.logger import Logger
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricHistogram


class _CircuitBreakerCacheDecorator(Cache):
    """Wraps a Cache with a circuit breaker.

    On CircuitOpenError: ``get`` returns None (graceful degradation),
    ``set``/``delete``/``exists`` become no-ops.
    """

    def __init__(self, inner: Cache, cb: CircuitBreakerInterface) -> None:
        """Wrap ``inner`` so each cache call passes through circuit breaker ``cb``."""
        self._inner = inner
        self._cb = cb

    async def get(self, key: str) -> bytes | None:
        """Read through the breaker, returning None when the circuit is open."""
        try:
            with self._cb:
                return await self._inner.get(key)
        except Exception as e:
            if self._is_circuit_open(e):
                return None
            raise

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Write through the breaker, becoming a no-op when the circuit is open."""
        try:
            with self._cb:
                await self._inner.set(key, value, ttl_seconds)
        except Exception as e:
            if self._is_circuit_open(e):
                return
            raise

    async def delete(self, key: str) -> None:
        """Delete through the breaker, becoming a no-op when the circuit is open."""
        try:
            with self._cb:
                await self._inner.delete(key)
        except Exception as e:
            if self._is_circuit_open(e):
                return
            raise

    async def exists(self, key: str) -> bool:
        """Probe through the breaker, reporting False when the circuit is open."""
        try:
            with self._cb:
                return await self._inner.exists(key)
        except Exception as e:
            if self._is_circuit_open(e):
                return False
            raise

    @staticmethod
    def _is_circuit_open(exc: Exception) -> bool:
        """Report whether an exception is the breaker's open-circuit signal."""
        return isinstance(exc, CircuitOpenError)


class _TimeoutCacheDecorator(Cache):
    """Wraps a Cache with an async timeout on each operation."""

    def __init__(self, inner: Cache, timeout: float) -> None:
        """Wrap ``inner`` so each cache call aborts after ``timeout`` seconds."""
        self._inner = inner
        self._timeout = timeout

    async def get(self, key: str) -> bytes | None:
        """Read with a deadline, raising AppTimeoutError when it elapses.

        Raises:
            AppTimeoutError: If the underlying get does not complete in time.

        """
        try:
            return await asyncio.wait_for(self._inner.get(key), timeout=self._timeout)
        except TimeoutError as e:
            msg = f"cache.get timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Write with a deadline, raising AppTimeoutError when it elapses.

        Raises:
            AppTimeoutError: If the underlying set does not complete in time.

        """
        try:
            await asyncio.wait_for(
                self._inner.set(key, value, ttl_seconds), timeout=self._timeout
            )
        except TimeoutError as e:
            msg = f"cache.set timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def delete(self, key: str) -> None:
        """Delete with a deadline, raising AppTimeoutError when it elapses.

        Raises:
            AppTimeoutError: If the underlying delete does not complete in time.

        """
        try:
            await asyncio.wait_for(self._inner.delete(key), timeout=self._timeout)
        except TimeoutError as e:
            msg = f"cache.delete timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e

    async def exists(self, key: str) -> bool:
        """Probe with a deadline, raising AppTimeoutError when it elapses.

        Raises:
            AppTimeoutError: If the underlying exists does not complete in time.

        """
        try:
            return await asyncio.wait_for(self._inner.exists(key), timeout=self._timeout)
        except TimeoutError as e:
            msg = f"cache.exists timed out after {self._timeout}s"
            raise AppTimeoutError(msg) from e


class _LoggingCacheDecorator(Cache):
    """Wraps a Cache with debug-level logging on get/set/delete."""

    def __init__(self, inner: Cache, logger: Logger) -> None:
        """Wrap ``inner`` so each cache call is logged via ``logger`` at debug level."""
        self._inner = inner
        self._logger = logger

    async def get(self, key: str) -> bytes | None:
        """Read and log the lookup along with its hit/miss outcome."""
        self._logger.debug("cache.get", key=key)
        result = await self._inner.get(key)
        if result is None:
            self._logger.debug("cache.miss", key=key)
        else:
            self._logger.debug("cache.hit", key=key)
        return result

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Write and log the key and TTL of the stored entry."""
        self._logger.debug("cache.set", key=key, ttl=ttl_seconds)
        await self._inner.set(key, value, ttl_seconds)

    async def delete(self, key: str) -> None:
        """Delete and log the removed key."""
        self._logger.debug("cache.delete", key=key)
        await self._inner.delete(key)

    async def exists(self, key: str) -> bool:
        """Probe and log the key together with its existence result."""
        result = await self._inner.exists(key)
        self._logger.debug("cache.exists", key=key, result=result)
        return result


class _MetricsCacheDecorator(Cache):
    """Wraps a Cache with hit/miss counter and latency histogram."""

    def __init__(
        self,
        inner: Cache,
        hits: MetricCounter,
        misses: MetricCounter,
        latency: MetricHistogram,
    ) -> None:
        """Wrap ``inner``, recording hit/miss counters and a latency histogram."""
        self._inner = inner
        self._hits = hits
        self._misses = misses
        self._latency = latency

    async def get(self, key: str) -> bytes | None:
        """Read while recording get latency and a hit-or-miss counter."""
        start = time.monotonic()
        result = await self._inner.get(key)
        self._latency.observe(time.monotonic() - start, operation="get")
        if result is not None:
            self._hits.increment(operation="get")
        else:
            self._misses.increment(operation="get")
        return result

    async def set(self, key: str, value: bytes, ttl_seconds: int = 300) -> None:
        """Write while recording set latency."""
        start = time.monotonic()
        await self._inner.set(key, value, ttl_seconds)
        self._latency.observe(time.monotonic() - start, operation="set")

    async def delete(self, key: str) -> None:
        """Delete while recording delete latency."""
        start = time.monotonic()
        await self._inner.delete(key)
        self._latency.observe(time.monotonic() - start, operation="delete")

    async def exists(self, key: str) -> bool:
        """Probe while recording exists latency."""
        start = time.monotonic()
        result = await self._inner.exists(key)
        self._latency.observe(time.monotonic() - start, operation="exists")
        return result


class CacheBuilder:
    """Fluent builder for composing cache decorators.

    Example::

        cache = (CacheBuilder(redis_cache)
            .with_logging(logger)
            .with_metrics(hits, misses, latency)
            .build())
    """

    def __init__(self, inner: Cache) -> None:
        """Start a builder around base cache ``inner`` with no decorators selected yet."""
        self._inner = inner
        self._logger: Logger | None = None
        self._metrics: tuple[MetricCounter, MetricCounter, MetricHistogram] | None = None
        self._cb: CircuitBreakerInterface | None = None
        self._timeout: float | None = None

    def with_logging(self, logger: Logger) -> CacheBuilder:
        """Add logging decorator."""
        self._logger = logger
        return self

    def with_metrics(
        self,
        hits: MetricCounter,
        misses: MetricCounter,
        latency: MetricHistogram,
    ) -> CacheBuilder:
        """Add metrics decorator."""
        self._metrics = (hits, misses, latency)
        return self

    def with_circuit_breaker(self, cb: CircuitBreakerInterface) -> CacheBuilder:
        """Add a circuit breaker decorator."""
        self._cb = cb
        return self

    def with_timeout(self, seconds: float) -> CacheBuilder:
        """Add a timeout decorator."""
        self._timeout = seconds
        return self

    def build(self) -> Cache:
        """Build the decorated cache.

        Chain: base -> circuit_breaker -> timeout -> metrics -> logging.
        """
        cache: Cache = self._inner
        if self._cb is not None:
            cache = _CircuitBreakerCacheDecorator(cache, self._cb)
        if self._timeout is not None:
            cache = _TimeoutCacheDecorator(cache, self._timeout)
        if self._metrics is not None:
            hits, misses, latency = self._metrics
            cache = _MetricsCacheDecorator(cache, hits, misses, latency)
        if self._logger is not None:
            cache = _LoggingCacheDecorator(cache, self._logger)
        return cache
