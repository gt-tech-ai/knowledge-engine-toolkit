"""Tests for the adaptive (AIMD) bulkhead + its factory selection.

Parity with Go's ``go/tests/unit/foundation_adaptive_bulkhead_test.go``: the self-tuning limit
shrinks under sustained latency and recovers, TryExecute rejects when saturated, and the factory
selects the adaptive backend by kind.
"""

from __future__ import annotations

import asyncio
from typing import cast

import pytest

from techai_webutils.foundation.resilience.bulkhead import (
    AdaptiveBulkhead,
    BulkheadConfig,
    BulkheadFullError,
    BulkheadKind,
    bulkhead_from_config,
)


@pytest.mark.asyncio
async def test_adaptive_limit_shrinks_under_latency_and_recovers() -> None:
    """The AIMD limit shrinks under sustained latency and grows back once latency subsides.

    Why this test is important:
        - The whole point of an adaptive limiter is to back off concurrency at a slowing backend
          and reclaim it on recovery; a limit that did not move with latency would be a fixed
          bulkhead with extra machinery.

    What it tests:
        - Four slow samples (RTT over the threshold) drive the limit below its initial value; five
          fast samples then grow it back above the shrunk value.
    """
    bh = AdaptiveBulkhead(
        min_concurrent=1,
        max_concurrent=20,
        initial_concurrent=10,
        rtt_threshold_seconds=0.01,
        backoff_ratio=0.5,
    )
    assert bh.limit == 10

    for _ in range(4):  # slow samples shrink the limit multiplicatively
        await bh.execute(lambda: asyncio.sleep(0.02))
    shrunk = bh.limit
    assert shrunk < 10, "sustained latency must shrink the adaptive limit"

    for _ in range(5):  # fast samples grow the limit back additively
        await bh.execute(lambda: asyncio.sleep(0))
    assert bh.limit > shrunk, "the limit recovers once latency subsides"


@pytest.mark.asyncio
async def test_adaptive_try_execute_rejects_when_full() -> None:
    """TryExecute rejects immediately once the current adaptive limit is saturated.

    Why this test is important:
        - TryExecute exists so a caller can shed rather than queue; it must reject the moment the
          limit is reached, or the "try" semantics are broken.

    What it tests:
        - With a limit of 1 occupied by an in-flight execute, a concurrent try_execute raises
          BulkheadFullError.
    """
    bh = AdaptiveBulkhead(
        min_concurrent=1,
        max_concurrent=1,
        initial_concurrent=1,
        rtt_threshold_seconds=3600,  # never treat the held slot as a slow sample
        backoff_ratio=0.9,
    )
    started = asyncio.Event()
    release = asyncio.Event()

    async def held() -> None:
        started.set()
        await release.wait()

    task = asyncio.create_task(bh.execute(held))
    await started.wait()  # the single slot is now occupied

    with pytest.raises(BulkheadFullError):
        await bh.try_execute(lambda: asyncio.sleep(0))

    release.set()
    await task


def test_bulkhead_from_config_selects_adaptive() -> None:
    """The factory selects the adaptive backend for ADAPTIVE (config-selects-impl contract).

    Why this test is important:
        - The adaptive limiter is opted into by a config kind; if the factory silently returned a
          semaphore bulkhead, the configured self-tuning behavior would never take effect.

    What it tests:
        - ``bulkhead_from_config`` with ``BulkheadKind.ADAPTIVE`` returns an ``AdaptiveBulkhead``.
    """
    bh = bulkhead_from_config(BulkheadConfig(kind=BulkheadKind.ADAPTIVE))
    assert isinstance(bh, AdaptiveBulkhead)


def test_bulkhead_from_config_unknown_kind_raises() -> None:
    """The factory raises on an unknown kind rather than silently returning a degenerate bulkhead.

    Why this test is important:
        - A factory that swallowed an unknown kind would ship a mis-selected policy silently;
          failing loudly surfaces the config error at startup.

    What it tests:
        - ``bulkhead_from_config`` raises ``ValueError`` for a kind outside the enum.
    """
    bad = BulkheadConfig(kind=cast("BulkheadKind", "bogus"))

    with pytest.raises(ValueError, match="unknown bulkhead kind"):
        bulkhead_from_config(bad)
