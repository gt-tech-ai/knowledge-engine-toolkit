"""A Logger decorator that keeps a fraction of the success lines and every problem line.

A high-volume consumer (a stage service handling millions of documents) writes a few ``info``
lines per message; at that rate the success lines alone set the log bill. ``SampledLogger``
keeps each ``debug`` and ``info`` line with probability ``rate`` and passes every ``warning``,
``error`` and ``exception`` line, so a log volume budget never hides a failure. ``rate`` 1.0
(the default everywhere) keeps every line: a pass-through. Applied at the composition root,
around the service's logger, like any decorator.
"""

from __future__ import annotations

from random import SystemRandom
from typing import TYPE_CHECKING

from techai_webutils.core.errors import InvalidInputError
from techai_webutils.core.interfaces.logger import Logger

if TYPE_CHECKING:
    from collections.abc import Callable


class SampledLogger(Logger):
    """Keeps ``rate`` of the debug and info lines and every warning, error and exception line."""

    def __init__(
        self,
        inner: Logger,
        rate: float,
        *,
        draw: Callable[[], float] | None = None,
    ) -> None:
        """Wrap ``inner``.

        Args:
            inner: The logger the kept lines go to.
            rate: The fraction of debug and info lines kept, 0.0 to 1.0.
            draw: Returns a uniform number in [0, 1); a line is kept when it is below
                ``rate``. Defaults to the OS random source.

        Raises:
            InvalidInputError: When ``rate`` is outside 0.0 to 1.0.

        """
        if not 0.0 <= rate <= 1.0:
            msg = f"log sample rate {rate} must be between 0.0 and 1.0"
            raise InvalidInputError(msg)
        self._inner = inner
        self._rate = rate
        self._draw = draw if draw is not None else SystemRandom().random

    def _keep(self) -> bool:
        """Return whether this success line is kept (always at rate 1.0, never at 0.0)."""
        return self._rate >= 1.0 or self._draw() < self._rate

    def debug(self, msg: str, **kwargs: object) -> None:
        """Log ``msg`` at debug, when sampled in."""
        if self._keep():
            self._inner.debug(msg, **kwargs)

    def info(self, msg: str, **kwargs: object) -> None:
        """Log ``msg`` at info, when sampled in."""
        if self._keep():
            self._inner.info(msg, **kwargs)

    def warning(self, msg: str, **kwargs: object) -> None:
        """Log ``msg`` at warning, always."""
        self._inner.warning(msg, **kwargs)

    def error(self, msg: str, **kwargs: object) -> None:
        """Log ``msg`` at error, always."""
        self._inner.error(msg, **kwargs)

    def exception(self, msg: str, **kwargs: object) -> None:
        """Log ``msg`` with the active exception, always."""
        self._inner.exception(msg, **kwargs)

    def bind(self, **kwargs: object) -> Logger:
        """Return a sampled logger over ``inner.bind(**kwargs)`` at the same rate.

        Returns:
            The bound, sampled logger.

        """
        return SampledLogger(self._inner.bind(**kwargs), self._rate, draw=self._draw)
