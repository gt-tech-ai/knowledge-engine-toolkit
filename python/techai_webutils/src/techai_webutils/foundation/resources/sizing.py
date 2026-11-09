"""Lane sizing: derive a lane's in-flight limit from the process's CPU and memory."""

from __future__ import annotations

import math
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.resource_probe import ResourceLimits


def lane_concurrency(
    limits: ResourceLimits, *, per_item_bytes: int, cap: int, reserve_bytes: int
) -> int:
    """Return how many items a lane may hold in flight.

    ``max(1, min(cap, cpu slots, memory slots))``.

    CPU slots are ``floor(limits.cpu)`` and memory slots ``(memory_bytes - reserve_bytes)
    // per_item_bytes``; an unbounded value (``math.inf`` CPU) leaves the cap in force.
    The result is never below 1, so a lane on a tiny pod still makes progress, one item at
    a time.

    Args:
        limits: The process's limits, from a ``ResourceProbe``.
        per_item_bytes: Peak memory one in-flight item uses (e.g. a parser's
            ``ParseCost``).
        cap: The lane's configured concurrency, now an upper bound.
        reserve_bytes: Memory held back for the process itself (runtime, buffers, the
            other lanes).

    Raises:
        AppValueError: ``per_item_bytes`` or ``cap`` is below 1, or ``reserve_bytes`` is
            negative.

    """
    if per_item_bytes < 1 or cap < 1 or reserve_bytes < 0:
        msg = (
            "lane_concurrency needs per_item_bytes >= 1, cap >= 1 and reserve_bytes "
            ">= 0, got "
            f"{per_item_bytes=}, {cap=}, {reserve_bytes=}"
        )
        raise AppValueError(msg)
    cpu_slots = cap if math.isinf(limits.cpu) else math.floor(limits.cpu)
    memory_slots = (limits.memory_bytes - reserve_bytes) // per_item_bytes
    return max(1, min(cap, cpu_slots, memory_slots))
