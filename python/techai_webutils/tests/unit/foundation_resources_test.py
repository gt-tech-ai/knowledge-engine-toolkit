"""Tests for the resource probes: the static and cgroup Kinds and their factory."""

from __future__ import annotations

import math
import os
from typing import TYPE_CHECKING, cast

import pytest

from techai_webutils.core.errors import AppValueError, ErrorCode, UnavailableError
from techai_webutils.core.interfaces.resource_probe import ResourceLimits, ResourceProbe
from techai_webutils.foundation.resources import (
    CgroupProbeConfig,
    ResourceProbeConfig,
    ResourceProbeKind,
    StaticProbeConfig,
    resource_probe_from_config,
)

if TYPE_CHECKING:
    from pathlib import Path

_GIB = 1024**3


def _cgroup(root: Path, *, cpu_max: str | None, memory_max: str | None) -> Path:
    """Write cgroup v2 ``cpu.max`` / ``memory.max`` files under ``root``.

    (``None`` omits a file).

    Returns:
        ``root``.

    """
    if cpu_max is not None:
        (root / "cpu.max").write_text(f"{cpu_max}\n")
    if memory_max is not None:
        (root / "memory.max").write_text(f"{memory_max}\n")
    return root


def _cgroup_probe(root: Path, *, required: bool = False) -> ResourceProbe:
    """Build a cgroup probe over ``root`` through the factory.

    Returns:
        A cgroup probe over ``root`` through the factory.

    """
    return resource_probe_from_config(
        ResourceProbeConfig(
            kind=ResourceProbeKind.CGROUP,
            cgroup=CgroupProbeConfig(root=str(root), required=required),
        )
    )


def _host_cpu() -> float:
    """Return the host CPU count the cgroup probe falls back to.

    Returns:
        The host CPU count the cgroup probe falls back to.

    """
    return float(os.cpu_count() or 1)


class TestStaticProbe:
    """The static Kind, the default."""

    def test_default_config_reports_no_limit(self) -> None:
        """The default probe is static and reports no limit.

        Why this test is important:
          - The static default must leave every lane at its configured cap, so adopting
            the probe changes neither throughput nor cost until a consumer selects cgroup.

        What it tests:
          - ``resource_probe_from_config(ResourceProbeConfig())`` reports
            ``ResourceLimits.unbounded()``.
        """
        probe = resource_probe_from_config(ResourceProbeConfig())

        assert probe.limits() == ResourceLimits.unbounded()
        assert math.isinf(probe.limits().cpu)

    def test_static_probe_reports_its_configured_values(self) -> None:
        """A static probe reports the CPU and memory its config names.

        Why this test is important:
          - Dev and tests size lanes from config values without touching the filesystem.

        What it tests:
          - ``StaticProbeConfig(cpu=4, memory_bytes=8 GiB)`` yields exactly those limits.
        """
        probe = resource_probe_from_config(
            ResourceProbeConfig(static=StaticProbeConfig(cpu=4.0, memory_bytes=8 * _GIB))
        )

        assert probe.limits() == ResourceLimits(cpu=4.0, memory_bytes=8 * _GIB)


class TestCgroupProbe:
    """The cgroup v2 Kind."""

    def test_reads_cpu_max_and_memory_max(self, tmp_path: Path) -> None:
        """The cgroup probe turns ``cpu.max`` quota/period and ``memory.max`` into limits.

        Why this test is important:
          - Lanes sized from the pod's real limits are what keep a pod from being
            OOM-killed or throttled when its requests and limits change.

        What it tests:
          - ``cpu.max = "200000 100000"`` reports 2.0 CPU; ``memory.max = 4 GiB`` reports
            4 GiB.
        """
        _cgroup(tmp_path, cpu_max="200000 100000", memory_max=str(4 * _GIB))

        assert _cgroup_probe(tmp_path).limits() == ResourceLimits(
            cpu=2.0, memory_bytes=4 * _GIB
        )

    def test_unlimited_values_fall_back_to_the_host(self, tmp_path: Path) -> None:
        """``max`` (no limit) falls back to the host's CPU count and physical memory.

        Why this test is important:
          - A pod with no limit set must still get a finite, sensible size, not an
            unbounded lane.

        What it tests:
          - ``cpu.max = "max 100000"`` and ``memory.max = "max"`` report
            ``os.cpu_count()`` CPUs and the host's physical memory.
        """
        _cgroup(tmp_path, cpu_max="max 100000", memory_max="max")

        limits = _cgroup_probe(tmp_path).limits()

        assert limits.cpu == _host_cpu()
        assert limits.memory_bytes == os.sysconf("SC_PAGE_SIZE") * os.sysconf(
            "SC_PHYS_PAGES"
        )

    def test_unreadable_files_fall_back_when_not_required(self, tmp_path: Path) -> None:
        """Missing cgroup files fall back to the host values.

        When the probe is not required.

        Why this test is important:
          - A developer laptop has no cgroup v2 files; the probe must not fail there.

        What it tests:
          - With no files under the root and ``required=False``, the probe reports the
            host CPU count.
        """
        assert _cgroup_probe(tmp_path).limits().cpu == _host_cpu()

    @pytest.mark.parametrize(
        ("cpu_max", "memory_max"),
        [
            (None, "1024"),
            ("100000 100000", None),
            ("garbage", "1024"),
            ("100000 100000", "lots"),
        ],
    )
    def test_required_probe_fails_loudly_when_unreadable(
        self, tmp_path: Path, cpu_max: str | None, memory_max: str | None
    ) -> None:
        """A required probe raises a coded UNAVAILABLE error for a missing.

        Or malformed file.

        Why this test is important:
          - In a cluster, a silent fallback to host values would size lanes from the node,
            not the pod, and get the pod OOM-killed; the consumer sets ``required`` there
            so it fails loudly.

        What it tests:
          - A missing or unparsable ``cpu.max`` or ``memory.max`` with ``required=True``
            raises ``UnavailableError`` (code ``UNAVAILABLE``).
        """
        _cgroup(tmp_path, cpu_max=cpu_max, memory_max=memory_max)

        with pytest.raises(UnavailableError) as raised:
            _cgroup_probe(tmp_path, required=True).limits()
        assert raised.value.code is ErrorCode.UNAVAILABLE


def test_unknown_kind_fails_loudly() -> None:
    """An unknown probe Kind is refused at construction.

    Why this test is important:
      - A typo in the consumer's config must stop start-up, not silently select a default.

    What it tests:
      - ``resource_probe_from_config`` with an unknown kind raises ``AppValueError``.
    """
    with pytest.raises(AppValueError, match="unknown resource probe kind"):
        resource_probe_from_config(
            ResourceProbeConfig(kind=cast("ResourceProbeKind", "bogus"))
        )
