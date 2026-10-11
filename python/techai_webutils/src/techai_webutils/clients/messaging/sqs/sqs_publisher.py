"""SQS message publisher using aiobotocore."""

from __future__ import annotations

import asyncio
import uuid
from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.core.errors import AppRuntimeError
from techai_webutils.core.interfaces.messaging import MessagePublisher

if TYPE_CHECKING:
    from types import TracebackType

    from aiobotocore.session import AioSession
    from types_aiobotocore_sqs import SQSClient
    from types_aiobotocore_sqs.type_defs import SendMessageBatchRequestEntryTypeDef

    from techai_webutils.clients.messaging.config import SQSConfig


class SQSPublisher(MessagePublisher):
    """Publishes messages to an SQS queue via aiobotocore.

    Usage::

        async with SQSPublisher(config) as pub:
            await pub.publish("events", b'{"type":"doc.uploaded"}')
    """

    def __init__(self, config: SQSConfig) -> None:
        """Store the SQS connection config; the client is created on context entry."""
        self._config = config
        self._client: SQSClient | None = None
        self._session: AioSession | None = None

    async def __aenter__(self) -> Self:
        """Open the underlying aiobotocore SQS client and return self."""
        self._session = aiobotocore.session.get_session()
        self._client = await self._session.create_client(
            "sqs",
            **self._config.client_kwargs(),
        ).__aenter__()
        return self

    async def __aexit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: TracebackType | None,
    ) -> None:
        """Close the underlying aiobotocore SQS client on context exit."""
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)

    async def publish(self, topic: str, payload: bytes) -> None:
        """Send a message to the configured SQS queue."""
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        await self._client.send_message(
            QueueUrl=self._config.queue_url,
            MessageBody=payload.decode("utf-8"),
            MessageAttributes={
                "topic": {"DataType": "String", "StringValue": topic},
            },
        )

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the configured SQS queue."""
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        entries: list[SendMessageBatchRequestEntryTypeDef] = [
            {
                "Id": str(uuid.uuid4()),
                "MessageBody": p.decode("utf-8"),
                "MessageAttributes": {
                    "topic": {"DataType": "String", "StringValue": topic},
                },
            }
            for p in payloads
        ]
        # SQS caps a batch at 10 messages; send the chunks concurrently rather than paying a full
        # round-trip per chunk in sequence — an outbox-relay burst of N>10 messages now
        # costs one round-trip, not ceil(N/10).
        await asyncio.gather(
            *(
                self._client.send_message_batch(
                    QueueUrl=self._config.queue_url,
                    Entries=entries[i : i + 10],
                )
                for i in range(0, len(entries), 10)
            )
        )
