"""Messaging client builder — ``new_messaging_from_config`` (shape; mirrors Go ``messaging.NewFromConfig``).

The tier-root factory: selects a message-broker backend by ``MessagingKind`` (SQS today) and returns
the ``MessagePublisher`` / ``MessageConsumer`` interfaces. The concrete backend lives in the ``sqs/``
subpackage, imported lazily so selecting a different kind never loads aiobotocore. Unknown kinds fail
loudly, matching the Go ``NewFromConfig`` contract. Every publish runs through the client tier under
``MessagingConfig.name``, and every handler call through the handler tier as ``<name>.handler``.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.client import ClientBase, handler
from techai_webutils.clients.messaging.decorated import decorate
from techai_webutils.clients.messaging.memory import (
    InMemoryBroker,
    InMemoryPublisher,
    InMemorySubscriber,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.decorate import Decorations

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import (
        MessageConsumer,
        MessagePublisher,
    )


class MessagingKind(StrEnum):
    """Which message-broker backend to build."""

    SQS = "sqs"
    """AWS SQS (real SQS in cloud, ElasticMQ in dev)."""
    MEMORY = "memory"
    """In-process broker (dev/test/all-stubs; no external broker)."""


@dataclass(frozen=True, slots=True)
class MessagingConfig:
    """Messaging configuration: the selected backend + its settings."""

    sqs: SQSConfig
    """SQS backend settings (endpoint, region, queue URL, credentials)."""
    kind: MessagingKind = MessagingKind.SQS
    """Selects the backend. The zero value is SQS."""
    name: str = "messaging"
    """The client's name in the decoration config's clients tier (and its metric, span and log
    label); its handlers run as ``<name>.handler``."""


def new_messaging_from_config(
    config: MessagingConfig,
    decorations: Decorations | None = None,
    *,
    broker: InMemoryBroker | None = None,
) -> MessagePublisher:
    """Build the ``MessagePublisher`` selected by ``config.kind``, its publishes decorated.

    Every publish runs through the client tier under ``config.name``, its layers resolved from
    ``decorations`` (None decorates nothing). ``broker`` connects a memory publisher to a memory
    subscriber; None gives the publisher its own. The SQS backend is imported lazily.
    """
    decorations = decorations or Decorations()
    return decorate(_publisher(config, broker), decorations, config.name)


def new_messaging_subscriber_from_config(
    config: MessagingConfig,
    decorations: Decorations | None = None,
    *,
    broker: InMemoryBroker | None = None,
) -> MessageConsumer:
    """Build the ``MessageConsumer`` selected by ``config.kind``, its handler calls decorated.

    Every handler call continues the publisher's trace and runs through the handler tier as
    ``<config.name>.handler`` (observed and bounded, never retried or broken), its layers resolved
    from ``decorations``. ``broker`` connects a memory subscriber to a memory publisher.
    """
    decorations = decorations or Decorations()
    handlers = ClientBase.wire(decorations, f"{config.name}.handler", handler())
    if config.kind is MessagingKind.SQS:
        # Lazy import: skip aiobotocore until selected.
        from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber  # noqa: PLC0415

        return SQSSubscriber(config.sqs, handlers)
    if config.kind is MessagingKind.MEMORY:
        return InMemorySubscriber(broker or InMemoryBroker(), handlers)
    msg = f"unknown messaging kind: {config.kind!r}"
    raise AppValueError(msg)


def _publisher(
    config: MessagingConfig, broker: InMemoryBroker | None
) -> MessagePublisher:
    """Build the undecorated publisher ``config.kind`` selects."""
    if config.kind is MessagingKind.SQS:
        # Lazy import: skip aiobotocore until selected.
        from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher  # noqa: PLC0415

        return SQSPublisher(config.sqs)
    if config.kind is MessagingKind.MEMORY:
        return InMemoryPublisher(broker or InMemoryBroker())
    msg = f"unknown messaging kind: {config.kind!r}"
    raise AppValueError(msg)
