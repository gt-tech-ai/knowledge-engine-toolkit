"""Static resource probe: limits taken from config, with no I/O."""

from __future__ import annotations

from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.resource_probe import ResourceLimits


class StaticResourceProbe:
    """A ``ResourceProbe`` that reports the limits it was built with."""

    def __init__(self, limits: ResourceLimits) -> None:
        """Hold ``limits`` for every ``limits()`` call."""
        self._limits = limits

    def limits(self) -> ResourceLimits:
        """Return the configured limits."""
        return self._limits
