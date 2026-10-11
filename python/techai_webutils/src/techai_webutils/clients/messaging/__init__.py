"""SQS messaging client (shape: ``builder.py`` + ``sqs/`` backend).

The concrete ``SQSPublisher``/``SQSSubscriber`` are intentionally NOT re-exported here so
importing the tier (for ``MessagingConfig`` / ``new_messaging_from_config``) does not eagerly
load aiobotocore — that is what the builder's lazy import is for. Import the concrete backends
from ``clients.messaging.sqs.sqs_publisher`` / ``.sqs_subscriber`` when needed.
"""

from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig

__all__ = [
    "MessagingConfig",
    "MessagingKind",
    "SQSConfig",
    "new_messaging_from_config",
    "new_messaging_subscriber_from_config",
]
