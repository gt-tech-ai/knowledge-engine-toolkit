"""Client-side adaptive-throttling interface.

Mirrors Go's ``interfaces.AdaptiveThrottler``. An ``AdaptiveThrottler`` rejects a growing
fraction of outbound requests locally when a backend's accept-rate drops (Google SRE
"Handling Overload"), shedding load before it reaches the struggling backend.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


@runtime_checkable
class AdaptiveThrottler(Protocol):
    """Sheds outbound load client-side when a backend starts failing.

    A peer resilience contract of ``Retrier``/``Bulkhead``: it wraps an operation with a policy
    and depends on no concrete implementation. Composes with the retry budget -- a locally
    throttled request is a terminal rejection, not retried.
    """

    async def do[T](self, op: Callable[[], Awaitable[T]]) -> T:
        """Run ``op`` unless the throttler rejects it locally.

        On local rejection, raises a coded "throttled" error WITHOUT invoking ``op``; otherwise
        ``op``'s outcome feeds the accept-rate window that drives the decision.
        """
        ...
