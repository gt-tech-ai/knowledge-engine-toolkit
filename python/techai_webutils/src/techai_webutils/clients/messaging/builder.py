"""Messaging client builder — ``new_messaging_from_config``.

(shape; mirrors Go ``messaging.NewFromConfig``).

The tier-root factory: selects a message-broker backend by ``MessagingKind`` (SQS today)
and returns the ``MessagePublisher`` / ``MessageConsumer`` interfaces. The concrete
backend lives in the ``sqs/`` subpackage, imported lazily so selecting a different kind
never loads aiobotocore. Unknown kinds fail loudly, matching the Go ``NewFromConfig``
contract. Every publish runs through the client tier under ``MessagingConfig.name``, and
every handler call through the handler tier as ``<name>.handler``.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.client import ClientBase, handler
from techai_webutils.clients.messaging.config import (
    DEFAULT_MAX_PAYLOAD_BYTES,
    DeliveryConfig,
)
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
    from techai_webutils.clients.messaging.sqs.sqs_publisher import (
        SQSPublisher as _LazySQSPublisher,
    )
    from techai_webutils.clients.messaging.sqs.sqs_subscriber import (
        SQSSubscriber as _LazySQSSubscriber,
    )
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
    """The client's name in the decoration config's clients tier (and its metric, span and
    log label); its handlers run as ``<name>.handler``."""
    delivery: DeliveryConfig = field(default_factory=DeliveryConfig)
    """Retry, dead-letter and processing-time settings for the subscriber (all unset by
    default)."""
    max_payload_bytes: int = DEFAULT_MAX_PAYLOAD_BYTES
    """The largest payload ``publish*`` accepts; a larger one is ``INVALID_INPUT`` before
    send."""


def new_messaging_from_config(
    config: MessagingConfig,
    decorations: Decorations | None = None,
    *,
    broker: InMemoryBroker | None = None,
) -> MessagePublisher:
    """Build the ``MessagePublisher`` selected by ``config.kind``.

    Its publishes decorated.

    Every publish runs through the client tier under ``config.name``, its layers resolved
    from ``decorations`` (None decorates nothing). ``broker`` connects a memory publisher
    to a memory subscriber; None gives the publisher its own. The SQS backend is imported
    lazily.

    Args:
        config: Selects the backend and carries its settings.
        decorations: Resolves the client tier's layers; ``None`` leaves the publisher
            undecorated.
        broker: The broker the memory kind shares; ``None`` makes a new one.

    Returns:
        The ``MessagePublisher`` selected by ``config.kind``.

    """
    decorations = decorations or Decorations()
    return decorate(_publisher(config, broker), decorations, config.name)


def new_messaging_subscriber_from_config(
    config: MessagingConfig,
    decorations: Decorations | None = None,
    *,
    broker: InMemoryBroker | None = None,
) -> MessageConsumer:
    """Build the ``MessageConsumer`` selected by ``config.kind``.

    Its handler calls decorated.

    Every handler call continues the publisher's trace and runs through the handler tier
    as ``<config.name>.handler`` (observed and bounded, never retried or broken), its
    layers resolved from ``decorations``. ``broker`` connects a memory subscriber to a
    memory publisher.

    Args:
        config: Selects the backend and carries its settings.
        decorations: Resolves the handler tier's layers; ``None`` runs handlers
            undecorated.
        broker: The broker the memory kind shares; ``None`` makes a new one.

    Returns:
        The ``MessageConsumer`` selected by ``config.kind``.

    Raises:
        AppValueError: Unknown messaging kind: ``config.kind``.

    """
    decorations = decorations or Decorations()
    handlers = ClientBase.wire(decorations, f"{config.name}.handler", handler())
    if config.kind is MessagingKind.SQS:
        # Lazy import: skip aiobotocore until selected.
        sqs_subscriber_cls = cast(
            "type[_LazySQSSubscriber]",
            importlib.import_module(
                "techai_webutils.clients.messaging.sqs.sqs_subscriber"
            ).SQSSubscriber,
        )

        return sqs_subscriber_cls(config.sqs, handlers, delivery=config.delivery)
    if config.kind is MessagingKind.MEMORY:
        return InMemorySubscriber(
            broker or InMemoryBroker(), handlers, delivery=config.delivery
        )
    msg = f"unknown messaging kind: {config.kind!r}"
    raise AppValueError(msg)


def _publisher(
    config: MessagingConfig, broker: InMemoryBroker | None
) -> MessagePublisher:
    """Build the undecorated publisher ``config.kind`` selects.

    Args:
        config: Selects the backend and carries its settings.
        broker: The broker the memory kind shares.

    Returns:
        The undecorated publisher ``config.kind`` selects.

    Raises:
        AppValueError: Unknown messaging kind: ``config.kind``.

    """
    if config.kind is MessagingKind.SQS:
        # Lazy import: skip aiobotocore until selected.
        sqs_publisher_cls = cast(
            "type[_LazySQSPublisher]",
            importlib.import_module(
                "techai_webutils.clients.messaging.sqs.sqs_publisher"
            ).SQSPublisher,
        )

        return sqs_publisher_cls(config.sqs, max_payload_bytes=config.max_payload_bytes)
    if config.kind is MessagingKind.MEMORY:
        return InMemoryPublisher(
            broker or InMemoryBroker(), max_payload_bytes=config.max_payload_bytes
        )
    msg = f"unknown messaging kind: {config.kind!r}"
    raise AppValueError(msg)
