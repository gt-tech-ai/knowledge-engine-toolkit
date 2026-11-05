"""SQS message publisher using aiobotocore.

Every message carries the current trace context (traceparent, tracestate, baggage) as string
message attributes beside ``topic``, so the subscriber continues the publisher's trace.
``publish_batch`` sends batches of ten with at most ``SQSConfig.publish_max_in_flight`` calls at
once, and checks each response's ``Failed`` list: rejected entries are resent once, and entries
rejected again raise a coded ``UNAVAILABLE`` error rather than being dropped.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.core.errors import AppRuntimeError, UnavailableError
from techai_webutils.core.interfaces.messaging import MessagePublisher
from techai_webutils.foundation.tracer.propagate import inject

if TYPE_CHECKING:
    from types import TracebackType

    from aiobotocore.session import AioSession
    from types_aiobotocore_sqs import SQSClient
    from types_aiobotocore_sqs.type_defs import (
        MessageAttributeValueTypeDef,
        SendMessageBatchRequestEntryTypeDef,
    )

    from techai_webutils.clients.messaging.config import SQSConfig


_SQS_BATCH_LIMIT = 10
"""Most entries one ``SendMessageBatch`` call accepts."""


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
            MessageAttributes=_attributes(topic),
        )

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the configured SQS queue, ten per call, bounded in flight.

        Raises:
            UnavailableError: Some entries were rejected twice; the rest were sent.

        """
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        entries: list[SendMessageBatchRequestEntryTypeDef] = [
            {
                "Id": str(i),
                "MessageBody": p.decode("utf-8"),
                "MessageAttributes": _attributes(topic),
            }
            for i, p in enumerate(payloads)
        ]
        # Batches go out concurrently (a burst costs about one round trip, not one per batch), but
        # at most publish_max_in_flight at once, so a large burst cannot exhaust the connection pool.
        gate = asyncio.Semaphore(self._config.publish_max_in_flight)
        rejected = await asyncio.gather(
            *(
                self._send_batch(entries[i : i + _SQS_BATCH_LIMIT], gate)
                for i in range(0, len(entries), _SQS_BATCH_LIMIT)
            )
        )
        lost = sum(rejected)
        if lost:
            msg = f"SQS rejected {lost} of {len(entries)} messages twice"
            raise UnavailableError(msg)

    async def _send_batch(
        self, entries: list[SendMessageBatchRequestEntryTypeDef], gate: asyncio.Semaphore
    ) -> int:
        """Send one batch, resend its rejected entries once, and return how many were rejected twice."""
        async with gate:
            failed = await self._send(entries)
            if failed:
                by_id = {entry["Id"]: entry for entry in entries}
                failed = await self._send([by_id[entry_id] for entry_id in failed])
        return len(failed)

    async def _send(
        self, entries: list[SendMessageBatchRequestEntryTypeDef]
    ) -> list[str]:
        """Send ``entries`` in one call and return the ids SQS rejected."""
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        resp = await self._client.send_message_batch(
            QueueUrl=self._config.queue_url, Entries=entries
        )
        return [failed["Id"] for failed in resp.get("Failed", [])]


def _attributes(topic: str) -> dict[str, MessageAttributeValueTypeDef]:
    """Return a message's string attributes: its topic and the current trace context."""
    carrier: dict[str, str] = {}
    inject(carrier)
    carrier["topic"] = topic
    return {k: {"DataType": "String", "StringValue": v} for k, v in carrier.items()}
