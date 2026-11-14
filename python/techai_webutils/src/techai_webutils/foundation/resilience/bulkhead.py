"""Bulkheads for concurrency limiting.

Two backends implement the ``Bulkhead`` ABC from core/interfaces: ``SemaphoreBulkhead`` (a
fixed-size ``asyncio.Semaphore``) and ``AdaptiveBulkhead`` (a self-tuning AIMD limit that
shrinks under latency and recovers). ``bulkhead_from_config`` selects between them by
``BulkheadKind``, mirroring Go's ``bulkhead.NewFromConfig`` (KindChannel / KindAdaptive).
A standalone ``SemaphoreBulkhead`` counts its refusals through ``with_recorder``; under a
decorate tier the bulkhead layer counts them instead.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.bulkhead import Bulkhead
from techai_webutils.foundation.resilience.observer import UNOBSERVED
from techai_webutils.foundation.resilience.series import Reason

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.foundation.resilience.series import Recorder


class BulkheadFullError(Exception):
    """Raised when the bulkhead has no available slots."""

    def __init__(self, message: str = "bulkhead full: no available slots") -> None:
        """Initialize with an explanatory message (defaulted for the common case).

        Args:
            message: What went wrong.

        """
        super().__init__(message)


@final
class SemaphoreBulkhead(Bulkhead):
    """Limits concurrent access using an asyncio semaphore."""

    def __init__(self, max_concurrent: int) -> None:
        """Size the underlying semaphore to the concurrency limit.

        Args:
            max_concurrent: Maximum number of executions allowed to run at once.

        """
        self._semaphore = asyncio.Semaphore(max_concurrent)
        self._recorder: Recorder = UNOBSERVED.recorder

    def with_recorder(self, recorder: Recorder) -> Self:
        """Count each refusal as a ``bulkhead_full`` rejection (operation "").

        Args:
            recorder: Where the refusals are counted.

        Returns:
            This bulkhead.

        """
        self._recorder = recorder
        return self

    @override
    async def execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn within the concurrency limit.

        Blocks until a slot is available.

        Args:
            fn: The op.

        Returns:
            What ``fn`` returns.

        """
        async with self._semaphore:
            return await fn()

    @override
    async def try_execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Attempt to run fn without blocking.

        Raises ``BulkheadFullError`` if no slots are available.

        Args:
            fn: The call to run if a slot is free now.

        Returns:
            What ``fn`` returns.

        Raises:
            BulkheadFullError: No slot is free.

        """
        if self._semaphore.locked():
            self._recorder.rejected("", Reason.BULKHEAD_FULL)
            raise BulkheadFullError
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

    # rtt_threshold_seconds marks the slow tail: a slower sample shrinks the limit
    # (ADAPTIVE only).
    rtt_threshold_seconds: float = 0.1
    """Latency (seconds) above which a sample shrinks the adaptive limit (ADAPTIVE
    only)."""

    # backoff_ratio in (0,1) is the multiplicative-decrease factor on a slow/failed
    # sample.
    backoff_ratio: float = 0.9
    """Multiplicative-decrease factor in (0,1) applied to the limit on a slow/failed
    sample."""


class AdaptiveBulkhead(Bulkhead):
    """A self-tuning (AIMD) concurrency-limiting Bulkhead.

    The limit adjusts to observed latency: a sample slower than ``rtt_threshold_seconds``
    -- or a failed op, a load-shed signal -- multiplicatively decreases the limit; a fast,
    successful sample additively increases it up to ``max_concurrent``. Mirrors Go's
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
        """Clamp the initial limit into ``[min, max]`` and start with no in-flight calls.

        Args:
            min_concurrent: The lowest limit.
            max_concurrent: The highest limit.
            initial_concurrent: The starting limit.
            rtt_threshold_seconds: A sample slower than this shrinks the limit; 0 disables
                it.
            backoff_ratio: What a failed or slow sample multiplies the limit by.

        """
        self._min: float = max(1.0, float(min_concurrent))
        self._max: float = max(self._min, float(max_concurrent))
        self._limit: float = min(self._max, max(self._min, float(initial_concurrent)))
        self._rtt_threshold: float = rtt_threshold_seconds
        self._backoff: float = backoff_ratio if 0 < backoff_ratio < 1 else 0.9
        self._in_flight: int = 0
        self._cond: asyncio.Condition = asyncio.Condition()
        self._recorder: Recorder | None = None

    def with_recorder(self, recorder: Recorder) -> Self:
        """Report the limit the bulkhead admits now and after each adjustment.

        Args:
            recorder: Where ``resilience_bulkhead_limit`` is set.

        Returns:
            This bulkhead.

        """
        self._recorder = recorder
        recorder.set_bulkhead_limit(float(self.limit))
        return self

    @property
    def limit(self) -> int:
        """Report the current adaptive concurrency limit (for metrics / observability)."""
        return int(self._limit)

    @override
    async def execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn within the current adaptive limit, blocking until a slot frees.

        Args:
            fn: The op.

        Returns:
            What ``fn`` returns.

        """
        async with self._cond:
            while self._in_flight >= int(self._limit):
                _ = await self._cond.wait()
            self._in_flight += 1
        return await self._run(fn)

    @override
    async def try_execute(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run fn only if a slot is immediately available.

        Otherwise raise ``BulkheadFullError``.

        Args:
            fn: The call to run if a slot is free now.

        Returns:
            What ``fn`` returns.

        Raises:
            BulkheadFullError: No slot is free.

        """
        async with self._cond:
            if self._in_flight >= int(self._limit):
                raise BulkheadFullError
            self._in_flight += 1
        return await self._run(fn)

    async def _run(self, fn: Callable[[], Awaitable[object]]) -> object:
        """Run the acquired op, then release its slot and adjust the limit by the sample.

        An op that did not complete (it failed or was cancelled) is a failed sample.

        Args:
            fn: The op.

        Returns:
            What ``fn`` returns.

        """
        start = time.monotonic()
        completed = False
        try:
            result = await fn()
            completed = True
        finally:
            await self._release(time.monotonic() - start, op_err=not completed)
        return result

    async def _release(self, rtt: float, *, op_err: bool) -> None:
        """Decrement in-flight, adjust the limit from the sample, and wake a waiter.

        Args:
            rtt: The op's duration.
            op_err: Whether it failed.

        """
        async with self._cond:
            self._in_flight = max(0, self._in_flight - 1)
            self._adjust(rtt, op_err=op_err)
            self._cond.notify_all()

    def _adjust(self, rtt: float, *, op_err: bool) -> None:
        """Apply AIMD to the limit, then report the limit it admits.

        A slow or failed sample shrinks it; a fast, successful one grows it.

        Args:
            rtt: The op's duration.
            op_err: Whether it failed.

        """
        if op_err or (self._rtt_threshold > 0 and rtt > self._rtt_threshold):
            self._limit = max(self._min, self._limit * self._backoff)
        else:
            self._limit = min(self._max, self._limit + 1)
        if self._recorder is not None:
            self._recorder.set_bulkhead_limit(float(self.limit))


def bulkhead_from_config(
    config: BulkheadConfig, recorder: Recorder | None = None
) -> Bulkhead:
    """Create a ``Bulkhead`` from config; raise ``ValueError`` on an unknown kind.

    Args:
        config: Selects the backend and carries its settings.
        recorder: Where an ADAPTIVE bulkhead reports its limit; the SEMAPHORE kind
            has no limit to report and its refusals are its decorate layer's.

    Returns:
        A ``Bulkhead`` from config; raise ``ValueError`` on an unknown kind.

    Raises:
        AppValueError: Unknown bulkhead kind: ``config.kind``.

    """
    if config.kind == BulkheadKind.SEMAPHORE:
        return SemaphoreBulkhead(config.max_concurrent)
    if config.kind == BulkheadKind.ADAPTIVE:
        adaptive = AdaptiveBulkhead(
            min_concurrent=config.min_concurrent,
            max_concurrent=config.max_concurrent,
            initial_concurrent=config.initial_concurrent,
            rtt_threshold_seconds=config.rtt_threshold_seconds,
            backoff_ratio=config.backoff_ratio,
        )
        return adaptive if recorder is None else adaptive.with_recorder(recorder)
    msg = f"unknown bulkhead kind: {config.kind}"
    raise AppValueError(msg)
