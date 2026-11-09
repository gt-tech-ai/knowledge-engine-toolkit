"""Circuit breaker pattern for fault isolation.

Tracks consecutive failures and opens the circuit when threshold is reached. In open
state, calls fail fast without executing the wrapped operation. Half-open admits at most
``half_open_max_calls`` trial calls at once (0: every caller), and each state change is
reported to ``on_transition``, so a decorator can export the state.
"""

from __future__ import annotations

import threading
import time
from enum import StrEnum
from typing import TYPE_CHECKING, Self, final, override

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
    """Probing after recovery timeout: the next call decides whether to close or
    re-open."""


class CircuitOpenError(Exception):
    """Raised when the circuit breaker is open."""

    def __init__(self, message: str = "circuit breaker is open") -> None:
        """Initialize with an explanatory message (defaulted for the common case)."""
        super().__init__(message)


@final
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
        *,
        half_open_max_calls: int = 0,
        on_transition: Callable[[CircuitState, CircuitState], None] | None = None,
    ) -> None:
        """Initialize the circuit breaker.

        Args:
            failure_threshold: Consecutive failures required to open the circuit.
            recovery_timeout: Seconds to wait before transitioning from open to half-open
                state.
            is_failure: Optional predicate classifying which exceptions count against the
                breaker. When set, an exception for which it returns ``False`` — a
                permanent domain error (NotFound/validation/auth) or a cancellation — is
                treated as a success so ordinary business outcomes never trip the breaker
                (mirrors the Go gobreaker ``IsSuccessful``). Defaults to ``None``, which
                counts every exception as a failure (unchanged behaviour).
            half_open_max_calls: The trial calls admitted at once while half-open; a
                caller past it fails fast as if open. 0 admits every caller (unchanged
                behaviour).
            on_transition: Called with ``(from, to)`` on every state change, under the
                breaker's lock, so it must be quick (a metric update).

        """
        self._failure_threshold = failure_threshold
        self._recovery_timeout = recovery_timeout
        self._is_failure = is_failure
        self._state = CircuitState.CLOSED
        self._failure_count = 0
        self._last_failure_time: float = 0.0
        self._half_open_max_calls = half_open_max_calls
        self._on_transition = on_transition
        self._trials = 0
        self._lock = threading.Lock()

    def _move(self, to: CircuitState) -> None:
        """Change to ``to`` and report the transition (caller holds the lock)."""
        if self._state is not to:
            previous, self._state = self._state, to
            if self._on_transition is not None:
                self._on_transition(previous, to)

    def _current(self) -> CircuitState:
        """Return the state, moving open to half-open once recovery elapsed.

        (caller holds the lock).

        Returns:
            The state, moving open to half-open once recovery elapsed.

        """
        if (
            self._state == CircuitState.OPEN
            and time.monotonic() - self._last_failure_time >= self._recovery_timeout
        ):
            self._move(CircuitState.HALF_OPEN)
        return self._state

    @property
    def state(self) -> CircuitState:
        """The current circuit state.

        Transitioning to half-open if recovery timeout elapsed.
        """
        with self._lock:
            return self._current()

    @override
    def execute(self, fn: Callable[[], object]) -> object:
        """Run ``fn`` through the breaker, exactly as the context-manager form gates it.

        Raises ``CircuitOpenError`` without calling ``fn`` while the circuit is open;
        otherwise returns ``fn``'s result and records its outcome.

        Returns:
            What ``fn`` returns.

        """
        with self:
            return fn()

    @override
    def __enter__(self) -> Self:
        """Enter the circuit breaker context.

        Raises ``CircuitOpenError`` immediately if the circuit is open, or half-open with
        ``half_open_max_calls`` trials already in flight, preventing the wrapped operation
        from executing. Otherwise the call is allowed through (half-open: as a trial).

        Returns:
            This breaker.

        Raises:
            CircuitOpenError: The circuit is open.

        """
        with self._lock:
            current = self._current()
            if current == CircuitState.OPEN:
                raise CircuitOpenError
            if current == CircuitState.HALF_OPEN and self._half_open_max_calls:
                if self._trials >= self._half_open_max_calls:
                    raise CircuitOpenError
                self._trials += 1
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
            self._trials = max(self._trials - 1, 0)
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
                self._move(CircuitState.CLOSED)
                return

            self._failure_count += 1
            self._last_failure_time = time.monotonic()
            if (
                self._failure_count >= self._failure_threshold
                or self._state == CircuitState.HALF_OPEN
            ):
                self._move(CircuitState.OPEN)
