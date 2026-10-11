"""Config-selected ``FactPublisher`` factory (the ``clients/llm/builder.py`` pattern).

``FactPublisherConfig.kind`` selects the ``stub`` (discard; the default, zero infrastructure) or the
``messaging`` backend (bounded batching sender over an injected ``MessagePublisher``). An unknown kind
or a misconfigured ``messaging`` kind raises a coded ``AppError(INVALID_INPUT)``. The messaging
backend is imported lazily so the stub path never loads it.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.facts.stub import StubFactPublisher
from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.fact_publisher import FactPublisher
    from techai_webutils.core.interfaces.messaging import MessagePublisher
    from techai_webutils.core.interfaces.metrics import MetricsProvider


class FactPublisherKind(StrEnum):
    """Which fact publisher implementation to build."""

    STUB = "stub"
    """Discard every fact (dev/test; no queue)."""
    MESSAGING = "messaging"
    """Batch facts onto a message queue through the injected ``MessagePublisher``."""


@dataclass(frozen=True, slots=True)
class FactPublisherConfig:
    """Fact publisher configuration."""

    kind: str = FactPublisherKind.STUB
    """Selects the backend (a ``FactPublisherKind`` value)."""
    queue: str = ""
    """Destination queue/topic for the ``messaging`` kind (required there)."""
    max_buffer: int = 1000
    """Most facts buffered in memory before new ones are dropped (``messaging``)."""
    flush_interval_s: float = 1.0
    """Longest wait to fill a batch of ten once its first fact arrived (``messaging``)."""
    drain_timeout_s: float = 5.0
    """Longest wait on shutdown for buffered facts to be sent (``messaging``)."""


def new_fact_publisher_from_config(
    config: FactPublisherConfig,
    *,
    publisher: MessagePublisher | None,
    metrics: MetricsProvider | None = None,
) -> FactPublisher:
    """Build the ``FactPublisher`` selected by ``config.kind``.

    Args:
        config: The publisher configuration.
        publisher: The message transport; required for the ``messaging`` kind.
        metrics: Provides ``gen_ai_fact_dropped_total{reason}`` for the ``messaging`` kind.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown kind, or a ``messaging`` kind without a
            publisher or a queue.

    """
    if config.kind == FactPublisherKind.STUB:
        return StubFactPublisher()
    if config.kind == FactPublisherKind.MESSAGING:
        if publisher is None:
            raise AppError(
                ErrorCode.INVALID_INPUT,
                "messaging fact publisher requires a MessagePublisher",
            )
        if not config.queue:
            raise AppError(
                ErrorCode.INVALID_INPUT, "messaging fact publisher requires a queue"
            )
        from techai_webutils.clients.facts.messaging import MessagingFactPublisher  # noqa: PLC0415

        return MessagingFactPublisher(
            publisher,
            config.queue,
            max_buffer=config.max_buffer,
            flush_interval_s=config.flush_interval_s,
            drain_timeout_s=config.drain_timeout_s,
            metrics=metrics,
        )
    raise AppError(
        ErrorCode.INVALID_INPUT, f"unknown fact publisher kind: {config.kind!r}"
    )
