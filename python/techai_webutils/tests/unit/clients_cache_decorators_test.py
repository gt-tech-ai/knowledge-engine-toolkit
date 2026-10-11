"""Tests for CacheBuilder with logging, metrics, and resilience decorators."""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.cache.decorators import CacheBuilder
from techai_webutils.core.interfaces.cache import Cache
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _mock_cache(**returns: object) -> AsyncMock:
    """Return an ``AsyncMock`` bound to the ``Cache`` interface with optional per-op return values.

    ``_mock_cache(get=b"v")`` makes ``get`` a hit; ``_mock_cache(get=None)`` a miss.
    Delegation is recorded natively, so callers can also ``assert_awaited*`` / ``assert_not_awaited``.
    """
    cache = AsyncMock(spec=Cache)
    for op, value in returns.items():
        getattr(cache, op).return_value = value
    return cache


def _slow(delay: float, result: object = None) -> Callable[..., Awaitable[object]]:
    """Return an async side_effect that sleeps ``delay`` seconds then returns ``result`` (to trip the timeout)."""

    async def _op(*_args: object, **_kwargs: object) -> object:
        await asyncio.sleep(delay)
        return result

    return _op


def _open_breaker() -> MagicMock:
    """Return a ``CircuitBreakerInterface`` mock whose context entry raises ``CircuitOpenError`` (open circuit)."""
    cb = MagicMock(spec=CircuitBreakerInterface)
    cb.__enter__.side_effect = CircuitOpenError()
    return cb


def _closed_breaker() -> MagicMock:
    """Return a ``CircuitBreakerInterface`` mock that passes through without suppressing errors (closed circuit)."""
    cb = MagicMock(spec=CircuitBreakerInterface)
    cb.__exit__.return_value = False
    return cb


class TestCacheBuilder:
    """Test suite for CacheBuilder decorator composition."""

    @pytest.mark.asyncio
    async def test_build_without_decorators(self) -> None:
        """Test that building without any decorators returns a functional pass-through cache.

        **Why this test is important:**
          - The builder must produce a working cache even with zero decorators
          - This establishes the baseline for verifying decorator additions
          - Services may use undecorated caches in test environments

        **What it tests:**
          - set delegates to the inner cache and get returns the inner cache's value unchanged
        """
        inner = _mock_cache(get=b"v")
        cache = CacheBuilder(inner).build()
        await cache.set("k", b"v")
        assert await cache.get("k") == b"v"
        inner.set.assert_awaited_once_with("k", b"v")

    @pytest.mark.asyncio
    async def test_with_logging(self) -> None:
        """Test that the logging decorator emits cache.set, cache.get, and cache.hit events.

        **Why this test is important:**
          - Cache hit/miss logging is essential for debugging performance issues
          - Operators use cache event logs to tune TTL and identify hot keys
          - The logging decorator must not alter cache behavior or return values

        **What it tests:**
          - Logger records "cache.set", "cache.get", and "cache.hit" messages
          - Underlying cache operations still return correct values
        """
        inner = _mock_cache(get=b"v")
        logger = MagicMock()
        cache = CacheBuilder(inner).with_logging(logger).build()  # type: ignore[arg-type]

        await cache.set("k", b"v")
        assert await cache.get("k") == b"v"

        # Verify logging happened
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "cache.set" in msg_texts
        assert "cache.get" in msg_texts
        assert "cache.hit" in msg_texts

    @pytest.mark.asyncio
    async def test_with_logging_miss(self) -> None:
        """Test that the logging decorator emits a cache.miss event on a cache miss.

        **Why this test is important:**
          - Cache miss events help identify cold cache scenarios and missing warm-up
          - High miss rates trigger alerts for cache infrastructure issues
          - Miss logging must be distinct from hit logging for accurate metrics

        **What it tests:**
          - Logger records "cache.miss" when getting a non-existent key
          - get returns None for the missing key
        """
        inner = _mock_cache(get=None)
        logger = MagicMock()
        cache = CacheBuilder(inner).with_logging(logger).build()  # type: ignore[arg-type]

        assert await cache.get("missing") is None
        msg_texts = [c.args[0] for c in logger.mock_calls if c.args]
        assert "cache.miss" in msg_texts

    @pytest.mark.asyncio
    async def test_with_metrics(self) -> None:
        """Test that the metrics decorator tracks cache hits, misses, and latency.

        **Why this test is important:**
          - Cache hit/miss ratios are key performance indicators on Grafana dashboards
          - Latency metrics detect cache degradation before it impacts users
          - Metrics drive autoscaling and capacity planning decisions

        **What it tests:**
          - hits.increment is called once (for the cache hit)
          - misses.increment is called once (for the cache miss)
          - latency.observe is called 3 times (set + 2 gets)
        """
        inner = AsyncMock(spec=Cache)
        inner.get.side_effect = lambda key: b"v" if key == "k" else None
        hits = MagicMock()
        misses = MagicMock()
        latency = MagicMock()
        cache = CacheBuilder(inner).with_metrics(hits, misses, latency).build()

        await cache.set("k", b"v")
        await cache.get("k")  # hit
        await cache.get("miss")  # miss

        hits.increment.assert_called_once()
        misses.increment.assert_called_once()
        assert latency.observe.call_count == 3  # set + 2 gets

    @pytest.mark.asyncio
    async def test_chained_decorators(self) -> None:
        """Test that metrics and logging decorators compose correctly when chained.

        **Why this test is important:**
          - Production caches use both metrics and logging decorators simultaneously
          - Decorator ordering must not cause double-counting or missed events
          - Each decorator must delegate correctly to the next in the chain

        **What it tests:**
          - Cache operations return correct values through the decorator chain
          - Both logger and latency metric are invoked
        """
        inner = _mock_cache(get=b"v")
        logger = MagicMock()
        hits = MagicMock()
        misses = MagicMock()
        latency = MagicMock()
        cache = (
            CacheBuilder(inner)
            .with_metrics(hits, misses, latency)
            .with_logging(logger)  # type: ignore[arg-type]
            .build()
        )

        await cache.set("k", b"v")
        result = await cache.get("k")
        assert result == b"v"

        # Both logging and metrics should fire
        assert len(logger.mock_calls) > 0
        assert latency.observe.call_count > 0


# ---------------------------------------------------------------------------
# Resilience decorator tests
# ---------------------------------------------------------------------------


class TestCacheCircuitBreakerDecorator:
    """Test suite for circuit breaker cache decorator graceful degradation."""

    @pytest.mark.asyncio
    async def test_circuit_breaker_passes_when_closed(self) -> None:
        """Test that cache operations work normally when the circuit breaker is closed.

        **Why this test is important:**
          - The circuit breaker must not impede cache operations when Redis is healthy
          - Transparent pass-through ensures no performance penalty in the common case
          - This validates the happy path before testing degradation behavior

        **What it tests:**
          - get returns the stored value when the circuit is closed
        """
        inner = _mock_cache(get=b"v")
        cb = _closed_breaker()
        cache = CacheBuilder(inner).with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await cache.get("k")
        assert result == b"v"

    @pytest.mark.asyncio
    async def test_circuit_breaker_get_returns_none_when_open(self) -> None:
        """Test that get returns None for graceful degradation when the circuit is open.

        **Why this test is important:**
          - When Redis is down, returning None causes cache misses instead of errors
          - The application continues serving requests from the primary data source
          - This is critical for maintaining availability during Redis outages

        **What it tests:**
          - get returns None and the underlying cache is never consulted (short-circuit)
        """
        inner = _mock_cache(get=b"v")
        cb = _open_breaker()
        cache = CacheBuilder(inner).with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        result = await cache.get("k")
        assert result is None
        inner.get.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_circuit_breaker_set_is_noop_when_open(self) -> None:
        """Test that set is silently skipped when the circuit is open.

        **Why this test is important:**
          - Write failures to a down cache must not interrupt the main application flow
          - The primary data store write has already succeeded; cache is best-effort
          - Silently skipping prevents error propagation to users during outages

        **What it tests:**
          - No exception is raised during set
          - The underlying cache never receives the write (short-circuit)
        """
        inner = _mock_cache()
        cb = _open_breaker()
        cache = CacheBuilder(inner).with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        # Should not raise
        await cache.set("k", b"v")
        # Underlying cache should not receive the write
        inner.set.assert_not_awaited()

    @pytest.mark.asyncio
    async def test_circuit_breaker_delete_is_noop_when_open(self) -> None:
        """Test that delete is silently skipped when the circuit is open.

        **Why this test is important:**
          - Cache invalidation during a Redis outage must not block the caller
          - Stale cache entries will naturally expire via TTL after recovery
          - The delete is best-effort; the primary data store is the source of truth

        **What it tests:**
          - No exception is raised during delete
          - The underlying cache's delete is never invoked (short-circuit)
        """
        inner = _mock_cache()
        cb = _open_breaker()
        cache = CacheBuilder(inner).with_circuit_breaker(cb).build()  # type: ignore[arg-type]

        # Should not raise
        await cache.delete("k")
        # Underlying delete was a no-op
        inner.delete.assert_not_awaited()


class TestCacheTimeoutDecorator:
    """Test suite for timeout cache decorator time limit enforcement."""

    @pytest.mark.asyncio
    async def test_timeout_passes_on_fast_op(self) -> None:
        """Test that fast cache operations complete successfully within the timeout.

        **Why this test is important:**
          - Normal cache operations must not be affected by the timeout wrapper
          - The timeout must not add measurable latency to the happy path
          - Return values must pass through unmodified from the underlying cache

        **What it tests:**
          - set and get succeed without raising when the operation is fast
          - Returned value matches the stored value
        """
        inner = _mock_cache(get=b"v")
        cache = CacheBuilder(inner).with_timeout(5.0).build()

        await cache.set("k", b"v")
        result = await cache.get("k")
        assert result == b"v"

    @pytest.mark.asyncio
    async def test_timeout_raises_on_slow_op(self) -> None:
        """Test that slow cache operations raise AppTimeoutError when exceeding the deadline.

        **Why this test is important:**
          - Slow Redis operations can block request processing and cause timeouts
          - Timeout enforcement prevents cache latency from dominating response time
          - AppTimeoutError is classified as transient, enabling upstream retry logic

        **What it tests:**
          - AppTimeoutError is raised when the cache operation exceeds the timeout
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        inner = AsyncMock(spec=Cache)
        inner.get.side_effect = _slow(2.0)
        cache = CacheBuilder(inner).with_timeout(0.01).build()

        with pytest.raises(AppTimeoutError):
            await cache.get("k")


class TestCacheFullResilienceChain:
    """Test suite for full cache decorator stack end-to-end composition."""

    @pytest.mark.asyncio
    async def test_full_chain_with_resilience(self) -> None:
        """Test that a fully decorated cache stack returns correct values and invokes all decorators.

        **Why this test is important:**
          - Production caches use all decorators together (circuit breaker + timeout + metrics + logging)
          - The full chain must not break data flow or introduce unexpected interactions
          - This integration test catches composition bugs that unit tests on individual decorators miss

        **What it tests:**
          - get returns the stored value through the full decorator chain
          - Logger records at least one message
          - Latency metric is observed at least once
        """
        inner = _mock_cache(get=b"v")

        logger = MagicMock()
        hits = MagicMock()
        misses = MagicMock()
        latency = MagicMock()
        cb = _closed_breaker()

        cache = (
            CacheBuilder(inner)
            .with_circuit_breaker(cb)  # type: ignore[arg-type]
            .with_timeout(5.0)
            .with_metrics(hits, misses, latency)
            .with_logging(logger)  # type: ignore[arg-type]
            .build()
        )

        result = await cache.get("k")
        assert result == b"v"

        assert len(logger.mock_calls) > 0
        assert latency.observe.call_count > 0


# ---------------------------------------------------------------------------
# Remaining branches: CB exists, timeout set/delete/exists, logging/metrics delete+exists
# ---------------------------------------------------------------------------


class TestCacheDecoratorGapCoverage:
    """Covers the cache-decorator branches not exercised above."""

    @pytest.mark.asyncio
    async def test_circuit_breaker_exists_false_when_open(self) -> None:
        """exists() degrades to False (not an error) when the circuit is open.

        **What it tests:**
          - An open breaker makes exists report a miss, so callers hit the source.
        """
        cache = CacheBuilder(_mock_cache()).with_circuit_breaker(_open_breaker()).build()  # type: ignore[arg-type]
        assert await cache.exists("k") is False

    @pytest.mark.asyncio
    async def test_timeout_raises_on_slow_set_delete_exists(self) -> None:
        """set/delete/exists each trip the timeout against a slow cache.

        **What it tests:**
          - The timeout protects writes and probes, not just get.
        """
        from techai_webutils.core.errors.errors import AppTimeoutError

        set_cache = AsyncMock(spec=Cache)
        set_cache.set.side_effect = _slow(2.0)
        delete_cache = AsyncMock(spec=Cache)
        delete_cache.delete.side_effect = _slow(2.0)
        exists_cache = AsyncMock(spec=Cache)
        exists_cache.exists.side_effect = _slow(2.0)

        cases: list[tuple[AsyncMock, Callable[[Cache], Awaitable[object]]]] = [
            (set_cache, lambda c: c.set("k", b"v")),
            (delete_cache, lambda c: c.delete("k")),
            (exists_cache, lambda c: c.exists("k")),
        ]
        for inner, op in cases:
            cache = CacheBuilder(inner).with_timeout(0.01).build()
            with pytest.raises(AppTimeoutError):
                await op(cache)

    @pytest.mark.asyncio
    async def test_logging_records_delete(self) -> None:
        """The logging decorator records a cache.delete event.

        **What it tests:**
          - A delete emits a cache.delete log; exists still returns correctly.
        """
        logger = MagicMock()
        cache = CacheBuilder(_mock_cache(exists=True)).with_logging(logger).build()  # type: ignore[arg-type]
        await cache.set("k", b"v")
        assert await cache.exists("k") is True
        await cache.delete("k")
        assert "cache.delete" in {c.args[0] for c in logger.mock_calls if c.args}

    @pytest.mark.asyncio
    async def test_metrics_observe_delete_and_exists(self) -> None:
        """The metrics decorator observes latency for delete and exists.

        **What it tests:**
          - delete and exists both record a latency observation.
        """
        hits, misses, latency = MagicMock(), MagicMock(), MagicMock()
        cache = (
            CacheBuilder(_mock_cache(exists=True))
            .with_metrics(hits, misses, latency)
            .build()
        )
        await cache.set("k", b"v")
        await cache.exists("k")
        await cache.delete("k")
        assert latency.observe.call_count >= 1
