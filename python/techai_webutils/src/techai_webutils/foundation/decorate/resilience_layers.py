"""The resilience layers, one middleware each (mirrors Go ``foundation/decorate``).

Timeout, retry, circuit breaker, bulkhead and rate limit: each ``new_*`` builds one
layer over its primitive and the ``Observer`` it reports its ``resilience_*`` events
through. ``Tier.middleware`` lays them out in the tier's order and binds each observer to
the tier and subject.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, final

from techai_webutils.core.errors.errors import (
    AppError,
    AppTimeoutError,
    ErrorCode,
    UnavailableError,
)
from techai_webutils.foundation.resilience.bulkhead import BulkheadFullError
from techai_webutils.foundation.resilience.circuit_breaker import CircuitOpenError
from techai_webutils.foundation.resilience.observer import UNOBSERVED
from techai_webutils.foundation.resilience.series import Reason

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.bulkhead import Bulkhead
    from techai_webutils.core.interfaces.circuit_breaker import CircuitBreakerInterface
    from techai_webutils.core.interfaces.rate_limiter import RateLimiter
    from techai_webutils.core.interfaces.retrier import Retrier
    from techai_webutils.foundation.decorate.middleware import OpMiddleware, OpThunk
    from techai_webutils.foundation.resilience.observer import Observer


@final
class _Timeout:
    """Bounds the op; expiry raises AppTimeoutError.

    An expiry of the layer's own deadline (``asyncio.timeout``, whose ``expired()`` says
    the layer cut the op) counts ``resilience_timeouts_total``; a TimeoutError the op
    raised itself (a socket timeout inside it) does not, whenever it arrives.
    """

    def __init__(self, seconds: float, observer: Observer) -> None:
        """Bind the deadline and what the layer reports through.

        Args:
            seconds: The deadline.
            observer: Where the layer reports its timeouts.

        """
        self._seconds = seconds
        self._observer = observer

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op under the deadline.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppTimeoutError: ``op`` timed out after ``self._seconds``s.

        """
        deadline = asyncio.timeout(self._seconds)
        try:
            async with deadline:
                return await next_()
        except TimeoutError as exc:
            if deadline.expired():
                self._observer.recorder.timed_out(op)
            msg = f"{op} timed out after {self._seconds}s"
            raise AppTimeoutError(msg) from exc


def new_timeout(seconds: float, observer: Observer | None = None) -> OpMiddleware:
    """Return the timeout layer: each operation gets ``seconds``.

    Under retry, each attempt does.

    Args:
        seconds: The deadline.
        observer: Where the layer reports its timeouts; ``None`` reports nothing.

    Returns:
        The timeout layer: each operation.

    """
    return _Timeout(seconds, observer or UNOBSERVED)


@final
class _Retry:
    """Runs the op through the injected Retrier.

    Once the retrier returns, the attempts it ran are counted (``Observer.retries_done``):
    the retries, then a budget refusal as a rejection only, else an exhaustion after a
    retry, with one Warning.
    """

    def __init__(self, retrier: Retrier, observer: Observer) -> None:
        """Bind the retrier and what the layer reports through.

        Args:
            retrier: The retry policy.
            observer: Where the layer reports its retries.

        """
        self._retrier = retrier
        self._observer = observer

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op with the retrier's policy.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            CancelledError: The caller cancelled the call; re-raised once its retries are
                counted (no exhaustion).

        """
        attempts = 0

        async def attempt() -> object:
            nonlocal attempts
            attempts += 1
            return await next_()

        try:
            result = await self._retrier.retry(attempt)
        except (Exception, asyncio.CancelledError) as exc:
            self._observer.retries_done(op, attempts, exc)
            raise
        self._observer.retries_done(op, attempts, None)
        return result


def new_retry(retrier: Retrier, observer: Observer | None = None) -> OpMiddleware:
    """Return the retry layer.

    Args:
        retrier: The retry policy.
        observer: Where the layer reports its retries; ``None`` reports nothing.

    Returns:
        The retry layer.

    """
    return _Retry(retrier, observer or UNOBSERVED)


@final
class _CircuitBreaker:
    """Runs the op inside the breaker.

    An open-breaker rejection becomes UnavailableError and counts a ``breaker_open``
    rejection; the dependency's own failure is not counted.
    """

    def __init__(
        self, cb: CircuitBreakerInterface, name: str, observer: Observer
    ) -> None:
        """Bind the breaker, the subject it guards and what the layer reports through.

        Args:
            cb: The breaker.
            name: The subject it guards.
            observer: Where the layer reports the calls the breaker refuses.

        """
        self._cb = cb
        self._name = name
        self._observer = observer

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op through the breaker.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            UnavailableError: Circuit breaker ``self._name`` open.

        """
        try:
            with self._cb:
                return await next_()
        except CircuitOpenError as exc:
            self._observer.recorder.rejected(op, Reason.BREAKER_OPEN)
            msg = f"circuit breaker {self._name} open"
            raise UnavailableError(msg) from exc


def new_circuit_breaker(
    cb: CircuitBreakerInterface, name: str, observer: Observer | None = None
) -> OpMiddleware:
    """Return the circuit-breaker layer for the subject ``name``.

    Args:
        cb: The breaker.
        name: The subject it guards.
        observer: Where the layer reports the calls the breaker refuses; ``None``
            reports nothing.

    Returns:
        The circuit-breaker layer for the subject ``name``.

    """
    return _CircuitBreaker(cb, name, observer or UNOBSERVED)


@final
class _Bulkhead:
    """Bounds the ops in flight: waits for a slot, or sheds when full.

    A shed op counts a ``bulkhead_full`` rejection; waiting records nothing.
    """

    def __init__(self, bulkhead: Bulkhead, *, shed: bool, observer: Observer) -> None:
        """Bind the bulkhead, whether a full one sheds and what the layer reports through.

        Args:
            bulkhead: Bounds the concurrent calls.
            shed: Whether a full bulkhead rejects instead of waiting.
            observer: Where the layer reports the calls it sheds.

        """
        self._bulkhead = bulkhead
        self._shed = shed
        self._observer = observer

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op inside the bulkhead.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppError: ``op``: concurrency limit reached.

        """
        if not self._shed:
            return await self._bulkhead.execute(next_)
        try:
            return await self._bulkhead.try_execute(next_)
        except BulkheadFullError as exc:
            self._observer.recorder.rejected(op, Reason.BULKHEAD_FULL)
            raise AppError(
                ErrorCode.RESOURCE_EXHAUSTED, f"{op}: concurrency limit reached"
            ) from exc


def new_bulkhead(
    bulkhead: Bulkhead, *, shed: bool, observer: Observer | None = None
) -> OpMiddleware:
    """Return the bulkhead layer.

    With ``shed``, a full bulkhead rejects (RESOURCE_EXHAUSTED).

    Args:
        bulkhead: Bounds the concurrent calls.
        shed: Whether a full bulkhead rejects instead of waiting.
        observer: Where the layer reports the calls it sheds; ``None`` reports nothing.

    Returns:
        The bulkhead layer.

    """
    return _Bulkhead(bulkhead, shed=shed, observer=observer or UNOBSERVED)


@final
class _RateLimit:
    """Rejects an op over the limiter's rate, counting a ``rate_limited`` rejection."""

    def __init__(self, limiter: RateLimiter, observer: Observer) -> None:
        """Bind the limiter and what the layer reports through.

        Args:
            limiter: The token bucket.
            observer: Where the layer reports the calls it refuses.

        """
        self._limiter = limiter
        self._observer = observer

    async def wrap_op(self, op: str, next_: OpThunk) -> object:
        """Run the op when the limiter allows it.

        Args:
            op: The operation.
            next_: Runs the rest of the chain.

        Returns:
            What the operation returns.

        Raises:
            AppError: ``op``: rate limit exceeded.

        """
        if not self._limiter.allow():
            self._observer.recorder.rejected(op, Reason.RATE_LIMITED)
            raise AppError(ErrorCode.RESOURCE_EXHAUSTED, f"{op}: rate limit exceeded")
        return await next_()


def new_rate_limit(
    limiter: RateLimiter, observer: Observer | None = None
) -> OpMiddleware:
    """Return the rate-limit layer: an op over the rate fails RESOURCE_EXHAUSTED at once.

    Args:
        limiter: The token bucket.
        observer: Where the layer reports the calls it refuses; ``None`` reports nothing.

    Returns:
        The rate-limit layer.

    """
    return _RateLimit(limiter, observer or UNOBSERVED)
