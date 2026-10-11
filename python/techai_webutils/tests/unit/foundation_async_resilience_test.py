"""Tests for async retry + async circuit breaker (the asyncio-path resiliency variants)."""

import asyncio

import pytest

from techai_webutils.core.errors.errors import (
    AppError,
    ErrorCode,
    InvalidInputError,
    UnavailableError,
)
from techai_webutils.foundation.resilience.async_circuit_breaker import (
    AsyncCircuitBreaker,
)
from techai_webutils.foundation.resilience.async_retry import (
    retry_after_s,
    retry_transient_async,
)
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitOpenError,
    CircuitState,
)


async def _fail_inside(cb: AsyncCircuitBreaker) -> None:
    """Raise ``ValueError("fail")`` inside the breaker, so it records one failure.

    Typed ``None``, not ``NoReturn``: the breaker is a context manager, so the type checker
    cannot rule out that it suppresses the error.
    """
    async with cb:
        msg = "fail"
        raise ValueError(msg)


class TestRetryTransientAsync:
    """Tests for ``retry_transient_async``."""

    @pytest.mark.asyncio
    async def test_retries_transient_then_succeeds(self) -> None:
        """Test that a coroutine raising a transient AppError is retried until it succeeds.

        **Why this test is important:**
          - The sync retry_transient cannot await a coroutine; the ingestion asyncio path needs an
            async retry or every transient blip (S3/Bedrock throttle) fails a document immediately.

        **What it tests:**
          - A coroutine raising UnavailableError twice then returning succeeds after 3 attempts.
        """
        attempts = 0

        @retry_transient_async(max_attempts=3, base_delay=0.001, max_delay=0.005)
        async def call() -> str:
            await asyncio.sleep(0)
            nonlocal attempts
            attempts += 1
            if attempts < 3:
                raise UnavailableError
            return "ok"

        assert await call() == "ok"
        assert attempts == 3

    @pytest.mark.asyncio
    async def test_does_not_retry_permanent(self) -> None:
        """Test that a permanent AppError is raised immediately without retry.

        **Why this test is important:**
          - Retrying a permanent error (malformed input) wastes time and delays DLQ routing; only
            transient errors should be retried.

        **What it tests:**
          - A coroutine raising InvalidInputError is attempted exactly once and the error propagates.
        """
        attempts = 0

        @retry_transient_async(max_attempts=3, base_delay=0.001)
        async def call() -> str:
            await asyncio.sleep(0)
            nonlocal attempts
            attempts += 1
            msg = "bad"
            raise InvalidInputError(msg)

        with pytest.raises(InvalidInputError):
            await call()
        assert attempts == 1

    @pytest.mark.asyncio
    async def test_injected_sleep_records_backoff_schedule(self) -> None:
        """Test that an injected sleep is used for the backoff (deterministic schedule recording).

        **Why this test is important:**
          - A caller that reuses ``retry_transient_async`` for its own backoff (e.g. stopping a stuck
            job) must be able to inject a deterministic sleep so the exponential-backoff schedule is
            testable (not real wall-clock sleeps) — keeping ONE source of truth for the backoff math (no
            hand-rolled loop).

        **What it tests:**
          - With an injected recording sleep, a coroutine that raises transient twice then succeeds
            invokes the injected sleep once per retry (2 non-negative delays recorded), succeeding on the
            3rd attempt — the real event loop never sleeps.
        """
        delays: list[float] = []

        async def _record_sleep(seconds: float) -> None:
            await asyncio.sleep(0)
            delays.append(seconds)

        attempts = 0

        @retry_transient_async(
            max_attempts=3, base_delay=0.01, max_delay=1.0, sleep=_record_sleep
        )
        async def call() -> str:
            await asyncio.sleep(0)
            nonlocal attempts
            attempts += 1
            if attempts < 3:
                raise UnavailableError
            return "ok"

        assert await call() == "ok"
        assert attempts == 3
        assert len(delays) == 2  # one sleep before each of the two retries
        assert all(delay >= 0 for delay in delays)

    @pytest.mark.asyncio
    async def test_honours_server_retry_pushback(self) -> None:
        """Test that a ``retry_after_ms`` detail stretches the wait to the server's delay, capped.

        **Why this test is important:**
          - A rate-limited server names the delay after which the call succeeds; retrying on the
            short exponential schedule only re-sends into the same exhausted window.

        **What it tests:**
          - a pushback of 2500 ms waits exactly 2.5 s (above the 0.01 s backoff)
          - a pushback of 60000 ms is capped at ``max_delay`` (5.0 s)
          - ``retry_after_s`` is None for a missing, non-numeric or negative detail and for a
            non-``AppError``
        """
        delays: list[float] = []

        async def _record_sleep(seconds: float) -> None:
            await asyncio.sleep(0)
            delays.append(seconds)

        pushbacks = iter(["2500", "60000"])

        @retry_transient_async(
            max_attempts=3, base_delay=0.01, max_delay=5.0, sleep=_record_sleep
        )
        async def call() -> str:
            await asyncio.sleep(0)
            delay = next(pushbacks, None)
            if delay is not None:
                raise AppError(
                    ErrorCode.UNAVAILABLE, "slow down", details={"retry_after_ms": delay}
                )
            return "ok"

        assert await call() == "ok"
        assert delays == [2.5, 5.0]
        assert retry_after_s(UnavailableError()) is None
        assert (
            retry_after_s(
                AppError(ErrorCode.UNAVAILABLE, "x", details={"retry_after_ms": "soon"})
            )
            is None
        )
        assert (
            retry_after_s(
                AppError(ErrorCode.UNAVAILABLE, "x", details={"retry_after_ms": "-5"})
            )
            is None
        )
        assert retry_after_s(RuntimeError("x")) is None


class TestAsyncCircuitBreaker:
    """Tests for the async circuit breaker."""

    @pytest.mark.asyncio
    async def test_opens_after_threshold_and_fails_fast(self) -> None:
        """Test that the breaker opens after N failures and then fails fast without running the body.

        **Why this test is important:**
          - When a downstream (Bedrock/gRPC) is down, an open breaker stops hammering it and fails
            fast, protecting both services; without it the consumer would pile up doomed calls.

        **What it tests:**
          - Two failures open the breaker; the next `async with` raises CircuitOpenError and the
            protected body never runs.
        """
        cb = AsyncCircuitBreaker(failure_threshold=2, recovery_timeout=60.0)
        for _ in range(2):
            with pytest.raises(ValueError, match="fail"):
                await _fail_inside(cb)

        ran = False
        with pytest.raises(CircuitOpenError):
            async with cb:
                ran = True
        assert ran is False

    @pytest.mark.asyncio
    async def test_success_keeps_circuit_closed(self) -> None:
        """Test that a successful call leaves the breaker closed.

        **Why this test is important:**
          - The breaker must not open on healthy traffic; a false-open would needlessly reject good
            requests.

        **What it tests:**
          - After a successful `async with`, current_state() is CLOSED.
        """
        cb = AsyncCircuitBreaker(failure_threshold=2)
        async with cb:
            pass
        assert await cb.current_state() is CircuitState.CLOSED
