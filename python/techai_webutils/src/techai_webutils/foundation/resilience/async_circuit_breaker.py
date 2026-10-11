"""Async circuit breaker for fault isolation.

The async analog of ``circuit_breaker.CircuitBreaker`` -- an ``async with`` context
manager backed by an ``asyncio.Lock`` so it composes with the asyncio ingestion path.
Reuses the sync module's ``CircuitState`` and ``CircuitOpenError``.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Self

from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitOpenError,
    CircuitState,
)

if TYPE_CHECKING:
    import types


class AsyncCircuitBreaker:
    """``async with``-based circuit breaker.

    Usage::

        cb = AsyncCircuitBreaker(failure_threshold=5, recovery_timeout=30.0)
        async with cb:
            result = await some_risky_call()

    Note: in HALF_OPEN this admits every concurrent caller as a trial (there is no
    single-probe gate), so a recovering downstream may see a burst of probes rather than
    one. That is acceptable for the low-fan-in async paths this guards; a single-probe
    half-open gate is a possible future enhancement.
    """

    def __init__(
        self,
        failure_threshold: int = 5,
        recovery_timeout: float = 30.0,
    ) -> None:
        """Initialize the breaker.

        Args:
            failure_threshold: Consecutive failures required to open the circuit.
            recovery_timeout: Seconds to wait before transitioning open -> half-open.

        """
        # _failure_threshold is the consecutive-failure count that opens the circuit.
        self._failure_threshold = failure_threshold
        # _recovery_timeout is the seconds to wait before open -> half-open.
        self._recovery_timeout = recovery_timeout
        # _state is the current circuit state (CLOSED/OPEN/HALF_OPEN).
        self._state = CircuitState.CLOSED
        # _failure_count is the running consecutive-failure count (reset on success).
        self._failure_count = 0
        # _last_failure_time is the monotonic timestamp of the most recent failure.
        self._last_failure_time: float = 0.0
        # _lock serializes state reads/writes across concurrent callers.
        self._lock = asyncio.Lock()

    async def current_state(self) -> CircuitState:
        """Return the current state, transitioning open -> half-open once recovery elapses."""
        async with self._lock:
            if (
                self._state == CircuitState.OPEN
                and time.monotonic() - self._last_failure_time >= self._recovery_timeout
            ):
                self._state = CircuitState.HALF_OPEN
            return self._state

    async def __aenter__(self) -> Self:
        """Enter the breaker; raise ``CircuitOpenError`` immediately if the circuit is open."""
        if await self.current_state() == CircuitState.OPEN:
            raise CircuitOpenError
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: types.TracebackType | None,
    ) -> bool:
        """Record the call outcome: success resets/closes, failure counts toward opening."""
        async with self._lock:
            if exc_type is None:
                self._failure_count = 0
                self._state = CircuitState.CLOSED
                return False

            self._failure_count += 1
            self._last_failure_time = time.monotonic()
            if self._failure_count >= self._failure_threshold:
                self._state = CircuitState.OPEN

        return False
