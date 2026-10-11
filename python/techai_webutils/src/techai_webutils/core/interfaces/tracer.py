"""Tracer interface for distributed tracing.

Defines the abstract contract for tracer implementations.
Foundation implementations satisfy this interface; consumers depend only on it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from contextlib import contextmanager
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Generator


class TracerProvider(ABC):
    """Abstract tracer for distributed tracing.

    Implementations: OpenTelemetry (default).
    All service code depends on this interface, never on a concrete tracing library.
    """

    @abstractmethod
    @contextmanager
    def span(
        self, name: str, **attributes: str | float | bool
    ) -> Generator[TracerSpan, None, None]:
        """Start a new span as a context manager.

        Usage::

            with tracer.span("operation", key="value") as span:
                span.set_attribute("result", 42)
        """
        ...

    @abstractmethod
    def shutdown(self) -> None:
        """Flush and shut down the tracer."""
        ...


class TracerSpan(ABC):
    """Abstract span within a trace."""

    @abstractmethod
    def set_attribute(self, key: str, value: str | float | bool) -> None:  # noqa: FBT001
        """Add a key-value attribute to the span."""
        ...

    @abstractmethod
    def record_error(self, error: BaseException) -> None:
        """Record an error on the span."""
        ...
