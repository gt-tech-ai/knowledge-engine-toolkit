"""Request-hedging primitive: fire a backup attempt to cut tail latency.

The default ``Hedger`` backends. Mirrors Go's ``foundation/resilience/hedge``. Hedging
duplicates load and is only safe for idempotent/read-only operations, so it is DISABLED by
default and opted into per read path via configuration (``HedgeKind.DELAY``).

Unlike the Go primitive (whose ``func() error`` op cannot be cancelled), the asyncio backend
genuinely cancels the loser attempt once the first responder returns.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.hedger import Hedger

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


class HedgeKind(StrEnum):
    """Selects a hedger backend."""

    # DISABLED runs the op exactly once (no hedging) -- the safe default.
    DISABLED = "disabled"
    """Run the operation exactly once — no backup attempt (the safe default)."""

    # DELAY fires a backup attempt after ``delay_seconds`` and takes the first responder.
    DELAY = "delay"
    """Fire a backup attempt after ``delay_seconds`` and take the first responder."""


@dataclass(frozen=True, slots=True)
class HedgeConfig:
    """Configuration for the hedger factory."""

    # kind selects the backend; DISABLED (the default) never hedges.
    kind: HedgeKind = HedgeKind.DISABLED
    """Which hedger backend to build; DISABLED (the default) never hedges."""

    # delay_seconds is how long to wait for the first attempt before firing the backup.
    delay_seconds: float = 0.05
    """Seconds to wait for the first attempt before firing the backup (DELAY only)."""


class DisabledHedger(Hedger):
    """A ``Hedger`` that runs the operation exactly once (no hedging) -- the safe default."""

    @override
    async def hedge[T](self, op: Callable[[], Awaitable[T]]) -> T:
        """Run ``op`` once, without a backup attempt."""
        return await op()


class DelayHedger(Hedger):
    """A delay-then-race ``Hedger``: fire a backup after a delay, take the first responder."""

    def __init__(self, delay_seconds: float) -> None:
        """Fire the backup attempt after ``delay_seconds`` without a first response."""
        self._delay: float = delay_seconds

    async def hedge[T](self, op: Callable[[], Awaitable[T]]) -> T:
        """Run ``op``; if it has not responded within the delay, race a backup and take the winner.

        The slower attempt is cancelled once the first responder returns, so the loser's work
        (e.g. an in-flight HTTP request) is actually stopped -- not merely abandoned.
        """
        first = asyncio.ensure_future(op())
        done, _pending = await asyncio.wait({first}, timeout=self._delay)
        if done:
            return first.result()  # first attempt beat the delay -- no backup needed

        backup = asyncio.ensure_future(op())
        try:
            done, _pending = await asyncio.wait(
                {first, backup},
                return_when=asyncio.FIRST_COMPLETED,
            )
            return next(iter(done)).result()
        finally:
            for task in (first, backup):
                if not task.done():
                    task.cancel()


def hedger_from_config(config: HedgeConfig) -> Hedger:
    """Create a ``Hedger`` from config; raise ``ValueError`` on an unknown kind."""
    if config.kind == HedgeKind.DISABLED:
        return DisabledHedger()
    if config.kind == HedgeKind.DELAY:
        return DelayHedger(config.delay_seconds)
    msg = f"unknown hedger kind: {config.kind}"
    raise ValueError(msg)
