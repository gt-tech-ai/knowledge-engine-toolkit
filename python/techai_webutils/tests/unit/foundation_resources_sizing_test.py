"""Tests for ``lane_concurrency``: a lane's in-flight limit derived from the process's resources."""

from __future__ import annotations

import math

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.resource_probe import ResourceLimits
from techai_webutils.foundation.resources import lane_concurrency

_GIB = 1024**3

_limits = st.builds(
    ResourceLimits,
    cpu=st.one_of(st.floats(min_value=0.0, max_value=512.0), st.just(math.inf)),
    memory_bytes=st.integers(min_value=0, max_value=2**50),
)


@given(
    limits=_limits,
    per_item_bytes=st.integers(min_value=1, max_value=64 * _GIB),
    cap=st.integers(min_value=1, max_value=4096),
    reserve_bytes=st.integers(min_value=0, max_value=64 * _GIB),
)
def test_lane_concurrency_never_exceeds_cap_cpu_or_memory(
    limits: ResourceLimits, per_item_bytes: int, cap: int, reserve_bytes: int
) -> None:
    """The derived concurrency respects the cap, the CPU count and the memory left after the reserve.

    Why this test is important:
      - A lane wider than its pod's CPU thrashes, and one wider than its memory gets the pod
        OOM-killed mid-batch; a lane of zero would stall forever.

    What it tests:
      - For any limits, cost, cap and reserve: 1 ≤ result ≤ cap; result ≤ floor(cpu) whenever that
        is ≥ 1; result ≤ (memory - reserve) // per_item_bytes whenever that is ≥ 1.
    """
    result = lane_concurrency(
        limits, per_item_bytes=per_item_bytes, cap=cap, reserve_bytes=reserve_bytes
    )

    assert 1 <= result <= cap
    if not math.isinf(limits.cpu) and math.floor(limits.cpu) >= 1:
        assert result <= math.floor(limits.cpu)
    memory_slots = (limits.memory_bytes - reserve_bytes) // per_item_bytes
    if memory_slots >= 1:
        assert result <= memory_slots


def test_unbounded_limits_run_at_the_cap() -> None:
    """Limits that constrain nothing leave the lane at its cap.

    Why this test is important:
      - The static probe's default reports no limit; the lane must then behave exactly as it did
        before the probe existed, at its configured concurrency.

    What it tests:
      - ``ResourceLimits.unbounded()`` with a 1 GiB cost and cap 8 yields 8.
    """
    assert (
        lane_concurrency(
            ResourceLimits.unbounded(), per_item_bytes=_GIB, cap=8, reserve_bytes=_GIB
        )
        == 8
    )


def test_the_tightest_limit_wins() -> None:
    """The smallest of the cap, the CPU slots and the memory slots is the result.

    Why this test is important:
      - Pins the formula on concrete numbers a reader can check by hand.

    What it tests:
      - 4.5 CPU, 10 GiB memory, 2 GiB reserve, 3 GiB per item, cap 16 → min(16, 4, 2) = 2.
    """
    limits = ResourceLimits(cpu=4.5, memory_bytes=10 * _GIB)

    assert (
        lane_concurrency(limits, per_item_bytes=3 * _GIB, cap=16, reserve_bytes=2 * _GIB)
        == 2
    )


@pytest.mark.parametrize(
    ("per_item_bytes", "cap", "reserve_bytes"),
    [(0, 4, 0), (1, 0, 0), (1, 4, -1)],
)
def test_invalid_sizing_inputs_are_refused(
    per_item_bytes: int, cap: int, reserve_bytes: int
) -> None:
    """A non-positive cost or cap, or a negative reserve, is refused.

    Why this test is important:
      - A zero cost divides by zero and a zero cap is a stalled lane; both are config mistakes that
        must fail at start-up.

    What it tests:
      - ``per_item_bytes=0``, ``cap=0`` and ``reserve_bytes=-1`` each raise ``AppValueError``.
    """
    with pytest.raises(AppValueError):
        lane_concurrency(
            ResourceLimits.unbounded(),
            per_item_bytes=per_item_bytes,
            cap=cap,
            reserve_bytes=reserve_bytes,
        )
