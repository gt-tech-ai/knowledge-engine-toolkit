"""Async circuit breaker for fault isolation.

The async analog of ``circuit_breaker.CircuitBreaker`` -- an ``async with`` context
manager backed by an ``asyncio.Lock`` so it composes with the asyncio ingestion path.
Reuses the sync module's ``CircuitState`` and ``CircuitOpenError``, and reports
through an ``Observer`` (``with_observer``) exactly as the sync breaker does.
"""

from __future__ import annotations

import asyncio
import time
from typing import TYPE_CHECKING, Self, final

from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitOpenError,
    CircuitState,
)
from techai_webutils.foundation.resilience.series import BreakerState

if TYPE_CHECKING:
    import types
    from collections.abc import Callable

    from techai_webutils.foundation.resilience.observer import Observer


@final
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
        *,
        counts: Callable[[BaseException], bool] | None = None,
    ) -> None:
        """Initialize the breaker.

        Args:
            failure_threshold: Consecutive failures required to open the circuit.
            recovery_timeout: Seconds to wait before transitioning open -> half-open.
            counts: Whether an exception is an outage; one it rejects (a caller's bad
                request) is recorded as a response, so it never opens a healthy
                circuit. ``None`` counts every exception.

        """
        # _counts decides which exceptions count toward opening the circuit.
        self._counts = counts
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
        # _observer receives the state and each move; None reports nothing.
        self._observer: Observer | None = None

    def with_observer(self, observer: Observer) -> Self:
        """Report the breaker's state, each move and one Warning per opening.

        It reports the current state at once. Set it before the first call: it does
        not take the lock, which only an awaiting caller can hold.

        Args:
            observer: Where the breaker reports.

        Returns:
            This breaker.

        """
        self._observer = observer
        observer.recorder.set_breaker_state(BreakerState(self._state.value))
        return self

    def _move(self, to: CircuitState) -> None:
        """Change to ``to`` and report the move (caller holds the lock).

        Args:
            to: The new state.

        """
        if self._state is to:
            return
        self._state = to
        if self._observer is None:
            return
        state = BreakerState(to.value)
        self._observer.recorder.set_breaker_state(state)
        self._observer.recorder.breaker_moved(state)
        if to is CircuitState.OPEN:
            self._observer.warn("circuit breaker opened")

    async def current_state(self) -> CircuitState:
        """Return the current state.

        Open moves to half-open once recovery elapses.

        Returns:
            The current state.

        """
        async with self._lock:
            if (
                self._state == CircuitState.OPEN
                and time.monotonic() - self._last_failure_time >= self._recovery_timeout
            ):
                self._move(CircuitState.HALF_OPEN)
            return self._state

    async def __aenter__(self) -> Self:
        """Enter the breaker.

        An open circuit raises ``CircuitOpenError`` immediately.

        Returns:
            This instance.

        Raises:
            CircuitOpenError: The circuit is open.

        """
        if await self.current_state() == CircuitState.OPEN:
            raise CircuitOpenError
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: types.TracebackType | None,
    ) -> None:
        """Record the call outcome: success resets/closes.

        A failure ``counts`` accepts counts toward opening; one it rejects is recorded as
        a success. The exception always propagates.

        Args:
            exc_type: The exception's type, if any.
            exc_val: The exception, if any.
            exc_tb: Its traceback, if any.

        """
        async with self._lock:
            if exc_val is None or (
                self._counts is not None and not self._counts(exc_val)
            ):
                self._failure_count = 0
                self._move(CircuitState.CLOSED)
                return

            self._failure_count += 1
            self._last_failure_time = time.monotonic()
            if self._failure_count >= self._failure_threshold:
                self._move(CircuitState.OPEN)
