"""Tests for the client-side adaptive throttler.

Parity with Go's ``go/tests/unit/foundation_adaptivethrottle_test.go``: the SRE rejection
probability rises while a backend fails and eases on recovery, and a shed request skips the
backend call entirely.
"""

from __future__ import annotations

import asyncio

import pytest

from techai_webutils.foundation.resilience.adaptive_throttler import (
    AdaptiveThrottler,
    ThrottledError,
)


@pytest.mark.asyncio
async def test_rejection_rises_on_failure_and_eases_on_recovery() -> None:
    """The SRE rejection probability climbs while a backend fails and falls once it recovers.

    Why this test is important:
        - Client-side throttling only protects a backend if the shed fraction tracks its health:
          rising while it rejects, easing as it recovers. A static probability would either never
          protect or never let traffic back.

    What it tests:
        - With shedding disabled (rand always allows), a healthy run keeps the probability at
          zero, a failing run drives it positive, and a recovery run brings it back down.
    """
    throttle = AdaptiveThrottler(
        k=2.0, decay=1.0, rand=lambda: 1.0
    )  # never shed → op always runs

    for _ in range(10):  # healthy: accepts keep pace with requests
        await throttle.do(_ok)
    assert throttle.rejection_probability == pytest.approx(0.0), (
        "a healthy backend is not throttled"
    )

    for _ in range(20):  # backend down: accepts frozen while requests climb
        with pytest.raises(RuntimeError):
            await throttle.do(_fail)
    fail_prob = throttle.rejection_probability
    assert fail_prob > 0.0, "sustained failure must raise the rejection probability"

    for _ in range(40):  # recovery: accepts climb again
        await throttle.do(_ok)
    assert throttle.rejection_probability < fail_prob, (
        "rejection eases as the backend recovers"
    )


@pytest.mark.asyncio
async def test_sheds_locally_without_calling_op() -> None:
    """Once the probability is positive, the throttler rejects locally without invoking op.

    Why this test is important:
        - The protection is worthless unless a shed request actually skips the backend call; if op
          still ran, the throttler would add latency without shedding load.

    What it tests:
        - After one failure builds a positive probability, a request whose draw falls under it
          raises ThrottledError and never calls op.
    """
    throttle = AdaptiveThrottler(
        k=2.0, decay=1.0, rand=lambda: 0.0
    )  # draw 0 → shed when p > 0

    # First call: probability is 0 at cold start, so it is allowed; its failure builds p > 0.
    with pytest.raises(RuntimeError):
        await throttle.do(_fail)

    called = False

    async def op() -> None:
        await asyncio.sleep(0)
        nonlocal called
        called = True

    with pytest.raises(ThrottledError):
        await throttle.do(op)
    assert called is False, "a shed request must not call op"


async def _ok() -> str:
    """Succeed like a backend call (feeds the accept window)."""
    await asyncio.sleep(0)
    return "ok"


async def _fail() -> str:
    """Fail like a backend call (does not feed the accept window)."""
    await asyncio.sleep(0)
    msg = "backend down"
    raise RuntimeError(msg)
