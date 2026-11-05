"""cgroup v2 resource probe: reads the pod's CPU quota and memory limit from ``cpu.max`` and ``memory.max``.

``cpu.max`` holds ``"<quota> <period>"`` (microseconds), or ``"max <period>"`` for no quota;
``memory.max`` holds a byte count, or ``"max"``. A value of ``max`` falls back to the host's CPU
count and physical memory. A missing or malformed file falls back the same way, unless the probe
is ``required`` (the consumer sets that in a cluster), in which case it raises ``UNAVAILABLE``.
"""

from __future__ import annotations

import os
from typing import TYPE_CHECKING

from techai_webutils.core.errors import UnavailableError
from techai_webutils.core.interfaces.resource_probe import ResourceLimits

if TYPE_CHECKING:
    from pathlib import Path

_UNLIMITED = "max"
"""The cgroup v2 token for "no limit"."""


def _host_cpu() -> float:
    """Return the host's CPU count (at least 1)."""
    return float(os.cpu_count() or 1)


def _host_memory_bytes() -> int:
    """Return the host's physical memory in bytes."""
    return os.sysconf("SC_PAGE_SIZE") * os.sysconf("SC_PHYS_PAGES")


def _parse_cpu_max(text: str) -> float | None:
    """Return the CPUs ``cpu.max`` allows, or ``None`` for no quota.

    Raises:
        ValueError: The text is not ``"<quota> <period>"`` or ``"max <period>"``.

    """
    quota, period = text.split()
    if quota == _UNLIMITED:
        return None
    return int(quota) / int(period)


def _parse_memory_max(text: str) -> int | None:
    """Return the bytes ``memory.max`` allows, or ``None`` for no limit.

    Raises:
        ValueError: The text is neither a byte count nor ``"max"``.

    """
    value = text.strip()
    return None if value == _UNLIMITED else int(value)


class CgroupResourceProbe:
    """A ``ResourceProbe`` over a cgroup v2 directory."""

    def __init__(self, root: Path, *, required: bool) -> None:
        """Read limits from ``root``; with ``required``, an unreadable file raises instead of falling back."""
        self._root = root
        self._required = required

    def limits(self) -> ResourceLimits:
        """Return the cgroup's CPU and memory limits, falling back to the host for ``max``.

        Raises:
            UnavailableError: A file is missing or malformed and the probe is required.

        """
        try:
            cpu = _parse_cpu_max((self._root / "cpu.max").read_text())
            memory = _parse_memory_max((self._root / "memory.max").read_text())
        except (OSError, ValueError) as exc:
            if self._required:
                msg = f"cannot read cgroup v2 limits under {self._root}: {exc}"
                raise UnavailableError(msg) from exc
            return ResourceLimits(cpu=_host_cpu(), memory_bytes=_host_memory_bytes())
        return ResourceLimits(
            cpu=_host_cpu() if cpu is None else cpu,
            memory_bytes=_host_memory_bytes() if memory is None else memory,
        )
