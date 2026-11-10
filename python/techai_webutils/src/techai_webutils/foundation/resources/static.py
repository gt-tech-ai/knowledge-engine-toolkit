"""Static resource probe: limits taken from config, with no I/O."""

from __future__ import annotations

from typing import TYPE_CHECKING, final

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.resource_probe import ResourceLimits


@final
class StaticResourceProbe:
    """A ``ResourceProbe`` that reports the limits it was built with."""

    def __init__(self, limits: ResourceLimits) -> None:
        """Hold ``limits`` for every ``limits()`` call.

        Args:
            limits: The limits to report.

        """
        self._limits = limits

    def limits(self) -> ResourceLimits:
        """Return the configured limits.

        Returns:
            The configured limits.

        """
        return self._limits
