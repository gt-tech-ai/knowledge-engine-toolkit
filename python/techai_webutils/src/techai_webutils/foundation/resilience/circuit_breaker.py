"""Circuit breaker pattern for fault isolation.

Tracks consecutive failures and opens the circuit when threshold is reached.
In open state, calls fail fast without executing the wrapped operation.
"""

from __future__ import annotations

import threading
import time
from enum import StrEnum
from typing import TYPE_CHECKING, Self, override

from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface

if TYPE_CHECKING:
    import types
    from collections.abc import Callable


class CircuitState(StrEnum):
    """Possible states of a circuit breaker."""

    CLOSED = "closed"
    """Normal operation: calls pass through and failures are counted."""
    OPEN = "open"
    """Tripped: calls fail fast without executing until the recovery timeout elapses."""
    HALF_OPEN = "half_open"
    """Probing after recovery timeout: the next call decides whether to close or re-open."""


class CircuitOpenError(Exception):
    """Raised when the circuit breaker is open."""

    def __init__(self, message: str = "circuit breaker is open") -> None:
        """Initialize with an explanatory message (defaulted for the common case)."""
        super().__init__(message)


class CircuitBreaker(CircuitBreakerInterface):
    """Context manager-based circuit breaker.

    Usage:
        cb = CircuitBreaker(failure_threshold=5, recovery_timeout=30.0)
        with cb:
            result = some_risky_call()
    """

    def __init__(
        self,
        failure_threshold: int = 5,
        recovery_timeout: float = 30.0,
        is_failure: Callable[[BaseException], bool] | None = None,
    ) -> None:
        """Initialize the circuit breaker.

        Args:
            failure_threshold: Consecutive failures required to open the circuit.
            recovery_timeout: Seconds to wait before transitioning from open
                to half-open state.
            is_failure: Optional predicate classifying which exceptions count against
                the breaker. When set, an exception for which it returns ``False`` —
                a permanent domain error (NotFound/validation/auth) or a cancellation —
                is treated as a success so ordinary business outcomes never trip the
                breaker (mirrors the Go gobreaker ``IsSuccessful``). Defaults to
                ``None``, which counts every exception as a failure (unchanged behaviour).

        """
        self._failure_threshold = failure_threshold
        self._recovery_timeout = recovery_timeout
        self._is_failure = is_failure
        self._state = CircuitState.CLOSED
        self._failure_count = 0
        self._last_failure_time: float = 0.0
        self._lock = threading.Lock()

    @property
    def state(self) -> CircuitState:
        """The current circuit state, transitioning to half-open if recovery timeout elapsed."""
        with self._lock:
            if (
                self._state == CircuitState.OPEN
                and time.monotonic() - self._last_failure_time >= self._recovery_timeout
            ):
                self._state = CircuitState.HALF_OPEN
            return self._state

    @override
    def execute(self, fn: Callable[[], object]) -> object:
        """Run ``fn`` through the breaker, exactly as the context-manager form gates it.

        Raises ``CircuitOpenError`` without calling ``fn`` while the circuit is open;
        otherwise returns ``fn``'s result and records its outcome.
        """
        with self:
            return fn()

    @override
    def __enter__(self) -> Self:
        """Enter the circuit breaker context.

        Raises ``CircuitOpenError`` immediately if the circuit is open,
        preventing the wrapped operation from executing. In closed or
        half-open state the call is allowed through.
        """
        current_state = self.state
        if current_state == CircuitState.OPEN:
            raise CircuitOpenError()
        return self

    @override
    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: types.TracebackType | None,
    ) -> None:
        """Exit the circuit breaker context and record the call outcome.

        On success the failure counter resets and the circuit closes. On
        failure the counter increments; once the threshold is reached the
        circuit transitions to open. The exception is never suppressed (the
        method returns ``None``, as the interface declares).
        """
        with self._lock:
            # A success — OR a non-failure exception (permanent domain error,
            # cancellation) when a classifier is configured — resets the breaker, so
            # ordinary business outcomes never trip it. Mirrors the Go gobreaker
            # IsSuccessful. With no classifier every exception counts.
            if exc_type is None or (
                exc_val is not None
                and self._is_failure is not None
                and not self._is_failure(exc_val)
            ):
                self._failure_count = 0
                self._state = CircuitState.CLOSED
                return

            self._failure_count += 1
            self._last_failure_time = time.monotonic()
            if self._failure_count >= self._failure_threshold:
                self._state = CircuitState.OPEN
