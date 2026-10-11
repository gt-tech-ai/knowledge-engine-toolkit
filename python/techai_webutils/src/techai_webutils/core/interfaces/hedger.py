"""Request-hedging interface for tail-latency reduction.

Mirrors Go's ``interfaces.Hedger``. A ``Hedger`` races a backup attempt to cut tail latency;
it is only safe for idempotent/read-only operations, so callers opt in per operation.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


@runtime_checkable
class Hedger(Protocol):
    """Reduces tail latency by racing a backup attempt.

    A peer resilience contract of ``Retrier``/``Bulkhead``: it wraps an operation with a policy
    and depends on no concrete implementation. Only safe for IDEMPOTENT operations -- a second
    attempt must not double a side effect.
    """

    async def hedge[T](self, op: Callable[[], Awaitable[T]]) -> T:
        """Run ``op``, racing a second concurrent attempt after the configured delay.

        Returns the result of the first attempt to complete; the slower attempt is cancelled.
        """
        ...
