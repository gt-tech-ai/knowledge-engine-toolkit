"""Tests for the request-hedging primitive + HedgeProxy.

Parity with Go's ``go/tests/unit/foundation_hedge_test.go``: the delay-then-race hedger lets a
fast backup beat a slow first attempt, the disabled hedger never duplicates the op, and the
factory fails loudly on an unknown kind.
"""

from __future__ import annotations

import asyncio
import time
from typing import cast

import pytest

from techai_webutils.clients.decorators.proxy import HedgeProxy
from techai_webutils.foundation.resilience.hedge import (
    DelayHedger,
    DisabledHedger,
    HedgeConfig,
    HedgeKind,
    hedger_from_config,
)


class _SlowThenFast:
    """A target whose first call lands in the slow tail and whose backup returns immediately."""

    def __init__(self) -> None:
        """Start with a zero call count."""
        self.calls: int = 0

    async def read(self) -> str:
        """Sleep on the first call (slow tail), return immediately on the backup."""
        self.calls += 1
        if self.calls == 1:
            await asyncio.sleep(0.5)
            return "slow"
        return "fast"


@pytest.mark.asyncio
async def test_delay_hedger_backup_beats_slow_first() -> None:
    """The hedger returns as soon as the fast backup completes, not waiting for the slow first.

    Why this test is important:
        - Hedging exists to collapse tail latency: when the first attempt lands in the slow tail,
          the backup must be able to win, or the primitive delivers no benefit.

    What it tests:
        - With a short delay, a first attempt sleeping far longer than the delay is beaten by an
          immediately-returning backup -- ``hedge`` returns the backup's result quickly.
    """
    target = _SlowThenFast()
    hedger = DelayHedger(delay_seconds=0.01)

    start = time.monotonic()
    result = await hedger.hedge(target.read)

    assert result == "fast", "the fast backup's result wins"
    assert time.monotonic() - start < 0.3, "must not wait for the slow first attempt"


@pytest.mark.asyncio
async def test_disabled_hedger_runs_once() -> None:
    """The disabled hedger runs the op exactly once (a non-idempotent path selects DISABLED).

    Why this test is important:
        - Hedging a non-idempotent op would double its side effect; the disabled default is the
          guard, and it must run the op exactly once.

    What it tests:
        - ``DisabledHedger`` invokes the op a single time and returns its result.
    """
    calls = 0

    async def op() -> str:
        await asyncio.sleep(0)
        nonlocal calls
        calls += 1
        return "once"

    result = await DisabledHedger().hedge(op)

    assert result == "once"
    assert calls == 1, "a disabled hedger must not duplicate the op"


def test_hedger_from_config_unknown_kind_raises() -> None:
    """The factory raises on an unknown kind rather than silently returning a degenerate hedger.

    Why this test is important:
        - A factory that swallowed an unknown kind would ship a mis-selected resilience policy
          silently; failing loudly surfaces the config error at startup.

    What it tests:
        - ``hedger_from_config`` raises ``ValueError`` for a kind outside the enum.
    """
    bad = HedgeConfig(kind=cast("HedgeKind", "bogus"), delay_seconds=0.05)

    with pytest.raises(ValueError, match="unknown hedger kind"):
        hedger_from_config(bad)


@pytest.mark.asyncio
async def test_hedge_proxy_hedges_async_calls() -> None:
    """HedgeProxy hedges an async method, so a slow first read is beaten by the backup.

    Why this test is important:
        - The proxy is how hedging composes into a client stack; it must actually race the call
          through the injected hedger, not merely forward it.

    What it tests:
        - A HedgeProxy over a slow-then-fast target, using a delay hedger, returns the backup's
          fast result.
    """
    proxied = HedgeProxy(_SlowThenFast(), DelayHedger(delay_seconds=0.01))

    result = await proxied.read()  # type: ignore[attr-defined]

    assert result == "fast"


def test_disabled_is_the_default_kind() -> None:
    """The default config disables hedging, so hedging is strictly opt-in.

    Why this test is important:
        - Hedging duplicates load; a default-on primitive would silently double every read. The
          safe default must be DISABLED.

    What it tests:
        - ``HedgeConfig()`` defaults to ``HedgeKind.DISABLED`` and ``hedger_from_config`` returns a
          non-duplicating hedger for it.
    """
    assert HedgeConfig().kind is HedgeKind.DISABLED
    assert isinstance(hedger_from_config(HedgeConfig()), DisabledHedger)
