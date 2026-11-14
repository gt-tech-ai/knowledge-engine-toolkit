"""Client-side adaptive throttler (Google SRE "Handling Overload").

Sheds a growing fraction of outbound requests locally when a backend's accept-rate drops,
protecting the backend from a client-side retry storm. Mirrors Go's
``foundation/resilience/adaptivethrottle``.

Compose it OUTERMOST of the retry budget (Throttle -> Retry -> op): a local rejection
raises ``ThrottledError`` before op runs, so the inner retrier never retries a shed
request -- the "not retried" property the retry budget relies on.

``with_recorder`` reports the rejection ratio after each call and counts each shed call
as a ``throttled`` rejection, as Go's ``Config.Recorder`` does.
"""

from __future__ import annotations

import asyncio
import random
from typing import TYPE_CHECKING, Self

from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.foundation.resilience.observer import UNOBSERVED
from techai_webutils.foundation.resilience.series import Reason

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.resilience.series import Recorder


class ThrottledError(AppError):
    """Raised when the throttler sheds a request locally without calling op.

    Coded UNAVAILABLE.
    """

    def __init__(
        self,
        message: str = (
            "adaptivethrottle: request shed by client-side adaptive throttling"
        ),
    ) -> None:
        """Initialize with the coded UNAVAILABLE classification and a message.

        The message explains the shed.

        Args:
            message: What went wrong.

        """
        super().__init__(ErrorCode.UNAVAILABLE, message)


class AdaptiveThrottler:
    """The SRE client-side adaptive throttler.

    Tracks a decaying window of ``requests`` (every attempt) and ``accepts`` (successes).
    The rejection probability is ``max(0, (requests - k*accepts) / (requests + 1))``: it
    climbs while a backend fails (accepts frozen, requests rising) and falls once it
    recovers.
    """

    def __init__(
        self,
        *,
        k: float = 2.0,
        decay: float = 0.98,
        rand: Callable[[], float] | None = None,
    ) -> None:
        """Configure the accept multiplier ``k``, window ``decay`` and RNG.

        ``decay`` is in (0,1]; the RNG draws rejections.

        Args:
            k: The accept multiplier: requests are shed once they exceed ``k`` times the
                accepts.
            decay: How much each update keeps of the previous counts.
            rand: Draws the shedding coin; ``None`` uses ``random.random``.

        """
        self._k: float = k if k > 0 else 2.0
        self._decay: float = decay if 0 < decay <= 1 else 0.98
        # SystemRandom keeps the security scanners' weak-PRNG rule clean without a
        # suppression; its one os.urandom read per attempt is negligible next to the
        # guarded remote call.
        self._rand: Callable[[], float] = (
            rand if rand is not None else random.SystemRandom().random
        )
        self._requests: float = 0.0
        self._accepts: float = 0.0
        self._lock: asyncio.Lock = asyncio.Lock()
        self._recorder: Recorder = UNOBSERVED.recorder

    def with_recorder(self, recorder: Recorder) -> Self:
        """Report the rejection ratio after each call and count each shed call.

        Args:
            recorder: Where the ratio and the ``throttled`` rejections are recorded.

        Returns:
            This throttler.

        """
        self._recorder = recorder
        return self

    @property
    def rejection_probability(self) -> float:
        """Report the current probability that a request is shed locally.

        For metrics and observability.
        """
        return self._rejection_probability()

    def _rejection_probability(self) -> float:
        """Compute the SRE rejection probability from the current window.

        Returns:
            The SRE rejection probability from the current window.

        """
        return max(0.0, (self._requests - self._k * self._accepts) / (self._requests + 1))

    async def do[T](self, op: Callable[[], Awaitable[T]]) -> T:
        """Run ``op`` unless the throttler sheds it locally (raising ``ThrottledError``).

        A success feeds the accept window; every attempt (shed or allowed) feeds the
        request window, so a sustained failure raises the rejection probability and a
        recovery lowers it. After each call the ratio, computed under the lock, is
        reported outside it, and a shed call is counted as ``throttled``.

        Args:
            op: The call to run; it is not started when the throttler rejects locally.

        Returns:
            What ``op`` returns.

        Raises:
            ThrottledError: The throttler shed the call.

        """
        async with self._lock:
            # Age the window, then decide from the pre-attempt ratio so a cold start never
            # sheds.
            self._requests *= self._decay
            self._accepts *= self._decay
            shed = self._rand() < self._rejection_probability()
            # This attempt counts as a request whether shed or allowed.
            self._requests += 1
            ratio = self._rejection_probability()

        if shed:
            self._recorder.set_throttle_rejection_ratio(ratio)
            self._recorder.rejected("", Reason.THROTTLED)
            raise ThrottledError

        try:
            result = await op()  # a failure propagates and is NOT counted as an accept
        except Exception:
            self._recorder.set_throttle_rejection_ratio(ratio)
            raise
        async with self._lock:
            self._accepts += 1
            ratio = self._rejection_probability()
        self._recorder.set_throttle_rejection_ratio(ratio)
        return result
