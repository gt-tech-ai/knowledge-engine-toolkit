"""Config-selected resource probe factory: ``ResourceProbeKind`` + config + ``resource_probe_from_config``.

``static`` (the default) reports the limits its config names and does no I/O; its defaults report
no limit, so a lane sized from them runs at its configured cap. ``cgroup`` reads the cgroup v2
files. The engine reads no environment: a consumer that runs in a cluster sets
``CgroupProbeConfig.required`` in its own config layer.
"""

from __future__ import annotations

import math
import sys
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.resource_probe import ResourceLimits
from techai_webutils.foundation.resources.cgroup import CgroupResourceProbe
from techai_webutils.foundation.resources.static import StaticResourceProbe

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.resource_probe import ResourceProbe


class ResourceProbeKind(StrEnum):
    """Which ``ResourceProbe`` to build."""

    STATIC = "static"
    """Limits from config, no I/O (dev, tests, and any consumer that sets its sizes by hand)."""
    CGROUP = "cgroup"
    """Limits read from the process's cgroup v2 files (a container with CPU and memory limits)."""


@dataclass(frozen=True, slots=True)
class StaticProbeConfig:
    """The static Kind's limits; the defaults constrain nothing."""

    cpu: float = math.inf
    """CPUs to report (``math.inf``: no limit)."""
    memory_bytes: int = sys.maxsize
    """Memory bytes to report (``sys.maxsize``: no limit)."""


@dataclass(frozen=True, slots=True)
class CgroupProbeConfig:
    """The cgroup Kind's settings."""

    root: str = "/sys/fs/cgroup"
    """The cgroup v2 directory holding ``cpu.max`` and ``memory.max``."""
    required: bool = False
    """Raise ``UNAVAILABLE`` when the files are unreadable, instead of falling back to the host
    values. A consumer sets it when running in a cluster, where the fallback would size lanes from
    the node instead of the pod."""


@dataclass(frozen=True, slots=True)
class ResourceProbeConfig:
    """Resource probe configuration."""

    kind: ResourceProbeKind = ResourceProbeKind.STATIC
    """Selects the probe."""
    static: StaticProbeConfig = field(default_factory=StaticProbeConfig)
    """The static Kind's limits."""
    cgroup: CgroupProbeConfig = field(default_factory=CgroupProbeConfig)
    """The cgroup Kind's settings."""


def resource_probe_from_config(config: ResourceProbeConfig) -> ResourceProbe:
    """Build the ``ResourceProbe`` selected by ``config.kind``.

    Raises:
        AppValueError: The kind is unknown.

    """
    if config.kind is ResourceProbeKind.STATIC:
        return StaticResourceProbe(
            ResourceLimits(cpu=config.static.cpu, memory_bytes=config.static.memory_bytes)
        )
    if config.kind is ResourceProbeKind.CGROUP:
        return CgroupResourceProbe(
            Path(config.cgroup.root), required=config.cgroup.required
        )
    msg = f"unknown resource probe kind: {config.kind!r}"
    raise AppValueError(msg)
