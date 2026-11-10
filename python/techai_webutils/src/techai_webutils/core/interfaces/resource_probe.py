"""Resource probe interface: the CPU and memory this process may use.

A lane sizes its concurrency from these limits (``foundation.resources.lane_concurrency``)
instead of a hand-set number, so a pod resized by its deployment needs no config change.
Implementations live in ``foundation.resources``: a static probe (values from config) and
a cgroup v2 probe.
"""

from __future__ import annotations

import math
import sys
from dataclasses import dataclass
from typing import Protocol, runtime_checkable


@dataclass(frozen=True, slots=True)
class ResourceLimits:
    """The CPU and memory available to this process."""

    cpu: float
    """CPUs this process may use (fractional for a cgroup quota; ``math.inf`` for no
    limit)."""
    memory_bytes: int
    """Bytes of memory this process may use (``sys.maxsize`` for no limit)."""

    @classmethod
    def unbounded(cls) -> ResourceLimits:
        """Return limits that constrain nothing.

        So a lane sized from them runs at its cap.

        Returns:
            Limits that constrain nothing.

        """
        return cls(cpu=math.inf, memory_bytes=sys.maxsize)


@runtime_checkable
class ResourceProbe(Protocol):
    """Reports the resource limits of the running process."""

    def limits(self) -> ResourceLimits:
        """Return the current limits.

        A probe configured to require the limits raises ``UnavailableError`` when it
        cannot read them.

        Returns:
            The CPU and memory limits the process runs under now.

        """
        ...
