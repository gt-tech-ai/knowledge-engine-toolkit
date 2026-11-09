"""Unit tests for resilience patterns.

This file tests that the retry decorator, circuit breaker, and async timeout
wrapper behave correctly under failure conditions, ensuring a service
degrades gracefully and recovers automatically.

# Test Coverage

The tests cover:
  - Retry decorator: retries transient errors up to max_attempts then succeeds
  - Retry decorator: does not retry permanent errors
  - Retry decorator: raises the last error after exhausting all attempts
  - Circuit breaker: opens after reaching the failure threshold
  - Circuit breaker: allows calls when in closed state
  - Circuit breaker: resets failure count on success
  - Async timeout: returns result when function completes in time
  - Async timeout: raises TimeoutError when function exceeds deadline

# Test Structure

Tests use pytest class-based organization grouped by resilience pattern (retry,
circuit breaker, timeout). Async tests use the pytest-asyncio marker. No external
services are mocked; tests use controlled failure functions.

# Running Tests

Run with: pytest tests/python/test_foundation/test_resilience.py
"""

import asyncio
from typing import NoReturn

import pytest
from prometheus_client import CollectorRegistry

from techai_webutils.core.errors.errors import (
    AppTimeoutError,
    NotFoundError,
    UnavailableError,
)
from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators
from techai_webutils.foundation.decorate.decorations import build
from techai_webutils.foundation.decorate.tier import Observability
from techai_webutils.foundation.metrics.metrics import new_metrics
from techai_webutils.foundation.resilience.circuit_breaker import (
    CircuitBreaker,
    CircuitOpenError,
)
from techai_webutils.foundation.resilience.retry import retry_transient
from techai_webutils.foundation.resilience.timeout import with_timeout

_EXPECTED_ATTEMPTS_COUNT = 3
_EXPECTED_SAMPLE = 2
_EXPECTED_REOPENED = 2


def _raise_unavailable() -> NoReturn:
    """Raise the transient error a failing protected call raises.

    Raises:
        UnavailableError: Fail.

    """
    msg = "fail"
    raise UnavailableError(msg)


class TestRetry:
    """Test suite for retry_transient decorator."""

    @staticmethod
    def test_retries_on_transient_error() -> None:
        """Test that retry_transient re-invokes the function on transient errors.

        **Why this test is important:**
          - Transient errors (network blips, temporary unavailability) are common in
            distributed systems
          - Without retry, every transient failure becomes a user-visible error
          - The retry count must be bounded to prevent infinite loops

        **What it tests:**
          - Function is called exactly max_attempts times before succeeding
          - Final return value is correct ("ok")
          - Attempts list length confirms retry count
        """
        attempts: list[int] = []

        @retry_transient(max_attempts=3, base_delay=0.01)
        def flaky() -> str:
            attempts.append(1)
            if len(attempts) < _EXPECTED_ATTEMPTS_COUNT:
                msg = "down"
                raise UnavailableError(msg)
            return "ok"

        result = flaky()
        assert result == "ok"
        assert len(attempts) == _EXPECTED_ATTEMPTS_COUNT

    @staticmethod
    def test_no_retry_on_permanent_error() -> None:
        """Test that retry_transient propagates permanent errors immediately.

        **Why this test is important:**
          - Retrying permanent errors (NOT_FOUND, INVALID_INPUT) wastes compute resources
          - Users experience unnecessary latency if permanent errors are retried
          - Correct classification prevents retry storms on bad requests

        **What it tests:**
          - NotFoundError is raised without retry
          - Function is called exactly once (no retries)
        """
        attempts: list[int] = []

        @retry_transient(max_attempts=3, base_delay=0.01)
        def always_missing() -> str:
            attempts.append(1)
            msg = "gone"
            raise NotFoundError(msg)

        with pytest.raises(NotFoundError):
            always_missing()
        assert len(attempts) == 1

    @staticmethod
    def test_raises_after_max_retries() -> None:
        """Test that retry_transient raises the last error after exhausting attempts.

        **Why this test is important:**
          - After max retries, the error must propagate so callers can handle it
          - Infinite retry would cause resource exhaustion and request timeouts
          - The raised error should be the most recent failure for accurate diagnostics

        **What it tests:**
          - UnavailableError is raised after all retry attempts are exhausted
        """

        @retry_transient(max_attempts=3, base_delay=0.01)
        def always_fails() -> str:
            msg = "permanently down"
            raise UnavailableError(msg)

        with pytest.raises(UnavailableError):
            always_fails()


class TestCircuitBreaker:
    """Test suite for CircuitBreaker state management."""

    @staticmethod
    def test_opens_after_threshold() -> None:
        """Test that CircuitBreaker opens after reaching the failure threshold.

        **Why this test is important:**
          - An open circuit prevents cascading failures to downstream services
          - Without circuit breaking, a failing dependency drags down all callers
          - The threshold must be configurable per-dependency based on SLA requirements

        **What it tests:**
          - After failure_threshold consecutive failures, CircuitOpenError is raised
          - Subsequent calls are rejected without invoking the protected function
        """
        cb = CircuitBreaker(failure_threshold=3, recovery_timeout=0.1)

        for _ in range(3):
            with pytest.raises(UnavailableError), cb:
                _raise_unavailable()

        # Now the circuit is open
        with pytest.raises(CircuitOpenError), cb:
            pass  # Should not execute

    @staticmethod
    def test_allows_calls_when_closed() -> None:
        """Test that CircuitBreaker allows calls through when in closed state.

        **Why this test is important:**
          - Normal operation must not be impeded by the circuit breaker
          - The closed state is the default healthy state for all circuit breakers
          - Incorrect initial state would block all calls on startup

        **What it tests:**
          - Code inside the circuit breaker context executes successfully
          - Result variable is set to "ok" confirming execution
        """
        cb = CircuitBreaker(failure_threshold=5, recovery_timeout=1.0)
        result = ""
        with cb:
            result = "ok"
        assert result == "ok"

    @staticmethod
    def test_resets_on_success() -> None:
        """Test that CircuitBreaker resets failure counter on success.

        **Why this test is important:**
          - Intermittent failures should not accumulate indefinitely toward the threshold
          - A single success indicates the dependency has recovered
          - Without reset, the circuit would eventually open even with high success rates

        **What it tests:**
          - After two failures and one success, the circuit remains closed
          - Subsequent calls still execute successfully (circuit did not open)
        """
        cb = CircuitBreaker(failure_threshold=3, recovery_timeout=1.0)

        # Two failures
        for _ in range(2):
            with pytest.raises(UnavailableError), cb:
                _raise_unavailable()

        # One success resets
        with cb:
            pass

        # Should still be able to call (not open)
        with cb:
            pass

    @staticmethod
    def test_implements_the_core_interface() -> None:
        """Test that CircuitBreaker is the core ``CircuitBreakerInterface``.

        And runs ``execute``.

        **Why this test is important:**
          - The client, cache and repository decorators accept a
            ``CircuitBreakerInterface``; the shipped breaker must satisfy that contract to
            be injected into them
          - ``execute`` is the interface's call path, so it must gate exactly like the
            context-manager form

        **What it tests:**
          - a ``CircuitBreaker`` is an instance of ``CircuitBreakerInterface``
          - ``execute`` returns the function's result while closed
          - once the threshold is reached, ``execute`` raises ``CircuitOpenError`` without
            calling the function
        """
        cb = CircuitBreaker(failure_threshold=1, recovery_timeout=60.0)
        calls: list[int] = []

        def _work() -> str:
            calls.append(1)
            return "ok"

        assert isinstance(cb, CircuitBreakerInterface)
        assert cb.execute(_work) == "ok"
        with pytest.raises(UnavailableError):
            cb.execute(_raise_unavailable)
        with pytest.raises(CircuitOpenError):
            cb.execute(_work)
        assert calls == [1]


class TestTimeout:
    """Test suite for async timeout wrapper."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_completes_within_timeout() -> None:
        """Test that with_timeout returns the result when completed before deadline.

        **Why this test is important:**
          - Normal fast operations must not be affected by the timeout wrapper
          - The return value must pass through unchanged from the wrapped coroutine
          - Zero overhead on the happy path ensures timeout wrapping is safe to apply
            broadly

        **What it tests:**
          - Result equals "done" (the awaitable's return value)
        """

        async def fast() -> str:
            await asyncio.sleep(0)
            return "done"

        result = await with_timeout(fast(), seconds=1.0)
        assert result == "done"

    @staticmethod
    @pytest.mark.asyncio
    async def test_raises_on_timeout() -> None:
        """Test that with_timeout raises TimeoutError when exceeding the deadline.

        **Why this test is important:**
          - Unbounded waits cause request pile-up and resource exhaustion
          - TimeoutError is classified as transient, enabling retry by upstream callers
          - Deadline enforcement is critical for maintaining SLA response times

        **What it tests:**
          - TimeoutError is raised when the awaitable exceeds the deadline
        """

        async def slow() -> str:
            await asyncio.sleep(10)
            return "never"

        with pytest.raises(AppTimeoutError):
            await with_timeout(slow(), seconds=0.05)


_DOWN = UnavailableError("dependency down")
"""A transient failure, which counts against a decorated breaker."""


def _sample(registry: CollectorRegistry, name: str, **labels: str) -> float:
    """Return a metric sample's value, 0 when the series does not exist.

    Returns:
        A metric sample's value, 0 when the series does not exist.

    """
    return registry.get_sample_value(name, labels) or 0.0


def _fail_through(breaker: CircuitBreakerInterface) -> None:
    """Run one call through ``breaker`` that fails transiently."""
    with breaker:
        raise _DOWN


def _probe_while_trial(breaker: CircuitBreakerInterface) -> None:
    """Hold the one admitted half-open trial, see a second caller fail fast.

    Then fail the trial.
    """
    with breaker:
        with pytest.raises(CircuitOpenError), breaker:
            pass
        raise _DOWN


class TestObservedCircuitBreaker:
    """A decorated breaker exports its state and lets only a bounded number.

    Of probes through.
    """

    @staticmethod
    def test_breaker_state_is_exported_and_half_open_is_rate_limited() -> None:
        """Test that a decorated breaker's state is exported.

        And half-open admits ``max_requests``.

        Why this test is important:
          - An open breaker that nobody can see sheds a dependency's calls silently, and a
            half-open breaker that admits every caller sends a burst of probes at a
            dependency that is just recovering.

        What it tests:
          - Tripping the breaker sets ``circuit_breaker_state`` to 2 and counts one
            transition to ``open``.
          - Half-open with ``max_requests=1`` admits one trial and fails a second caller
            fast; the trial's failure reopens the breaker (state 2, a second ``open``).
        """
        registry = CollectorRegistry()
        layers = decoration.Layers(
            circuit_breaker=decoration.CircuitBreaker(
                enabled=True, consecutive_failures=1, timeout=0.0, max_requests=1
            )
        )
        collab = Collaborators(observability=Observability(metrics=new_metrics(registry)))
        breaker = build("nlp", layers, collab).breaker
        assert breaker is not None
        assert _sample(registry, "circuit_breaker_state", dependency="nlp") == 0

        with pytest.raises(UnavailableError):
            _fail_through(breaker)
        opened = _sample(
            registry, "circuit_breaker_transitions_total", dependency="nlp", to="open"
        )
        assert opened == 1

        with pytest.raises(UnavailableError):
            _probe_while_trial(breaker)

        assert (
            _sample(registry, "circuit_breaker_state", dependency="nlp")
            == _EXPECTED_SAMPLE
        )
        probes = _sample(
            registry,
            "circuit_breaker_transitions_total",
            dependency="nlp",
            to="half_open",
        )
        assert probes == 1
        reopened = _sample(
            registry, "circuit_breaker_transitions_total", dependency="nlp", to="open"
        )
        assert reopened == _EXPECTED_REOPENED
