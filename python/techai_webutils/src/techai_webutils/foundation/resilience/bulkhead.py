"""Bulkheads for concurrency limiting.

Two backends implement the ``Bulkhead`` ABC from core/interfaces: ``SemaphoreBulkhead`` (a
fixed-size ``asyncio.Semaphore``) and ``AdaptiveBulkhead`` (a self-tuning AIMD limit that shrinks
under latency and recovers). ``bulkhead_from_config`` selects between them by
``BulkheadKind``, mirroring Go's ``bulkhead.NewFromConfig`` (KindChannel / KindAdaptive).
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.bulkhead import Bulkhead

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class BulkheadFullError(Exception):
    """Raised when the bulkhead has no available slots."""

    def __init__(self, message: str = "bulkhead full: no available slots") -> None:
        """Initialize with an explanatory message (defaulted for the common case)."""
        super().__init__(message)


class SemaphoreBulkhead(Bulkhead):
    """Limits concurrent access using an asyncio semaphore.

    Args:
        max_concurrent: Maximum number of concurrent executions allowed.

    """

    def __init__(self, max_concurrent: int) -> None:
        """Size the underlying semaphore to the concurrency limit.

        Args:
            max_concurrent: Maximum number of executions allowed to run at once.

        """
        self._semaphore = asyncio.Semaphore(max_concurrent)
        self._max_concurrent = max_concurrent

    async def execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn within the concurrency limit.

        Blocks until a slot is available.
        """
        async with self._semaphore:
            return await fn()

    async def try_execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Attempt to run fn without blocking.

        Raises ``BulkheadFullError`` if no slots are available.
        """
        if not self._semaphore._value:  # noqa: SLF001
            raise BulkheadFullError()
        async with self._semaphore:
            return await fn()


class BulkheadKind(StrEnum):
    """Selects a bulkhead backend."""

    # SEMAPHORE is a fixed-size concurrency limit (the default).
    SEMAPHORE = "semaphore"
    """Fixed-size concurrency limit backed by an ``asyncio.Semaphore`` (the default)."""

    # ADAPTIVE is a self-tuning (AIMD) limit that shrinks under latency.
    ADAPTIVE = "adaptive"
    """Self-tuning AIMD limit that shrinks under latency and recovers."""


@dataclass(frozen=True, slots=True)
class BulkheadConfig:
    """Configuration for the bulkhead factory."""

    # kind selects the backend; SEMAPHORE (the default) is a fixed-size limit.
    kind: BulkheadKind = BulkheadKind.SEMAPHORE
    """Which bulkhead backend to build; SEMAPHORE (the default) is a fixed-size limit."""

    # max_concurrent is the fixed limit (SEMAPHORE) or the adaptive ceiling (ADAPTIVE).
    max_concurrent: int = 10
    """Fixed concurrency limit (SEMAPHORE) or the adaptive ceiling (ADAPTIVE)."""

    # min_concurrent is the adaptive floor the limit never drops below (ADAPTIVE only).
    min_concurrent: int = 1
    """Adaptive floor the limit never drops below (ADAPTIVE only)."""

    # initial_concurrent is the adaptive limit's starting value (ADAPTIVE only).
    initial_concurrent: int = 5
    """Adaptive limit's starting value, clamped into [min, max] (ADAPTIVE only)."""

    # rtt_threshold_seconds marks the slow tail: a slower sample shrinks the limit (ADAPTIVE only).
    rtt_threshold_seconds: float = 0.1
    """Latency (seconds) above which a sample shrinks the adaptive limit (ADAPTIVE only)."""

    # backoff_ratio in (0,1) is the multiplicative-decrease factor on a slow/failed sample.
    backoff_ratio: float = 0.9
    """Multiplicative-decrease factor in (0,1) applied to the limit on a slow/failed sample."""


class AdaptiveBulkhead(Bulkhead):
    """A self-tuning (AIMD) concurrency-limiting Bulkhead.

    The limit adjusts to observed latency: a sample slower than ``rtt_threshold_seconds`` -- or a
    failed op, a load-shed signal -- multiplicatively decreases the limit; a fast, successful
    sample additively increases it up to ``max_concurrent``. Mirrors Go's
    ``foundation/resilience/bulkhead/adaptive``.
    """

    def __init__(
        self,
        *,
        min_concurrent: int,
        max_concurrent: int,
        initial_concurrent: int,
        rtt_threshold_seconds: float,
        backoff_ratio: float,
    ) -> None:
        """Clamp the initial limit into ``[min, max]`` and start with no in-flight calls."""
        self._min: float = max(1.0, float(min_concurrent))
        self._max: float = max(self._min, float(max_concurrent))
        self._limit: float = min(self._max, max(self._min, float(initial_concurrent)))
        self._rtt_threshold: float = rtt_threshold_seconds
        self._backoff: float = backoff_ratio if 0 < backoff_ratio < 1 else 0.9
        self._in_flight: int = 0
        self._cond: asyncio.Condition = asyncio.Condition()

    @property
    def limit(self) -> int:
        """Report the current adaptive concurrency limit (for metrics / observability)."""
        return int(self._limit)

    async def execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn within the current adaptive limit, blocking until a slot frees."""
        async with self._cond:
            while self._in_flight >= int(self._limit):
                await self._cond.wait()
            self._in_flight += 1
        return await self._run(fn)

    async def try_execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn only if a slot is immediately available; else raise ``BulkheadFullError``."""
        async with self._cond:
            if self._in_flight >= int(self._limit):
                raise BulkheadFullError
            self._in_flight += 1
        return await self._run(fn)

    async def _run(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run the acquired op, then release the slot and adjust the limit from its sample."""
        start = time.monotonic()
        op_err = False
        try:
            return await fn()
        except Exception:
            op_err = True
            raise
        finally:
            await self._release(time.monotonic() - start, op_err=op_err)

    async def _release(self, rtt: float, *, op_err: bool) -> None:
        """Decrement in-flight, adjust the limit from the sample, and wake a waiter."""
        async with self._cond:
            self._in_flight = max(0, self._in_flight - 1)
            self._adjust(rtt, op_err=op_err)
            self._cond.notify_all()

    def _adjust(self, rtt: float, *, op_err: bool) -> None:
        """Apply AIMD: a slow/failed sample shrinks the limit; a fast, successful one grows it."""
        if op_err or (self._rtt_threshold > 0 and rtt > self._rtt_threshold):
            self._limit = max(self._min, self._limit * self._backoff)
        else:
            self._limit = min(self._max, self._limit + 1)


def bulkhead_from_config(config: BulkheadConfig) -> Bulkhead:
    """Create a ``Bulkhead`` from config; raise ``ValueError`` on an unknown kind."""
    if config.kind == BulkheadKind.SEMAPHORE:
        return SemaphoreBulkhead(config.max_concurrent)
    if config.kind == BulkheadKind.ADAPTIVE:
        return AdaptiveBulkhead(
            min_concurrent=config.min_concurrent,
            max_concurrent=config.max_concurrent,
            initial_concurrent=config.initial_concurrent,
            rtt_threshold_seconds=config.rtt_threshold_seconds,
            backoff_ratio=config.backoff_ratio,
        )
    msg = f"unknown bulkhead kind: {config.kind}"
    raise AppValueError(msg)
