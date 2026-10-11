"""No-op tracer implementation.

All tracing operations are silent no-ops. Useful for tests, CLI tools,
and environments where distributed tracing is not configured.
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.tracer import TracerProvider, TracerSpan

if TYPE_CHECKING:
    from collections.abc import Generator


class NullTracerProvider(TracerProvider):
    """No-op tracer provider that produces null spans."""

    @contextmanager
    @override
    def span(
        self,
        name: str,
        **attributes: str | float | bool,
    ) -> Generator[TracerSpan, None, None]:
        """Yield a no-op span."""
        yield _NullSpan()

    def shutdown(self) -> None:
        """No-op shutdown."""


class _NullSpan(TracerSpan):
    """No-op span that silently accepts all operations."""

    def set_attribute(self, key: str, value: str | float | bool) -> None:  # noqa: FBT001
        """No-op."""

    def record_error(self, error: BaseException) -> None:
        """No-op."""
