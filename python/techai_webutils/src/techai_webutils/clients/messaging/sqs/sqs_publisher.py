"""SQS message publisher using aiobotocore.

Every message carries the current trace context (traceparent, tracestate, baggage) as
string message attributes beside ``topic``, so the subscriber continues the publisher's
trace. ``publish_batch`` sends batches of ten with at most
``SQSConfig.publish_max_in_flight`` calls at once, and checks each response's ``Failed``
list: rejected entries are resent once, and entries rejected again raise a coded
``UNAVAILABLE`` error rather than being dropped. A keyed publish carries its key as the
``partition_key`` string attribute; a standard queue does not order by it, so
``ordered_per_key`` is ``False``.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.clients.messaging.config import DEFAULT_MAX_PAYLOAD_BYTES
from techai_webutils.clients.messaging.payload import check_payload_size
from techai_webutils.core.errors import AppRuntimeError, UnavailableError
from techai_webutils.core.interfaces.messaging import (
    PARTITION_KEY_ATTRIBUTE,
    KeyedPublisher,
)
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


class SQSPublisher(KeyedPublisher):
    """Publishes messages to an SQS queue via aiobotocore.

    Usage::

        async with SQSPublisher(config) as pub:
            await pub.publish("events", b'{"type":"doc.uploaded"}')
    """

    def __init__(
        self, config: SQSConfig, *, max_payload_bytes: int = DEFAULT_MAX_PAYLOAD_BYTES
    ) -> None:
        """Store the SQS connection config; the client is created on context entry.

        A payload over ``max_payload_bytes`` is rejected before it is sent.
        """
        self._config = config
        self._max_payload_bytes = max_payload_bytes
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
        await self._send_one(topic, payload, _attributes(topic))

    async def publish_keyed(self, topic: str, key: str, payload: bytes) -> None:
        """Send a message to the configured SQS queue with ``key``.

        As its ``partition_key``.
        """
        await self._send_one(topic, payload, _attributes(topic, key))

    async def publish_batch(self, topic: str, payloads: list[bytes]) -> None:
        """Send multiple messages to the configured SQS queue, ten per call.

        Bounded in flight.

        Raises:
            UnavailableError: Some entries were rejected twice; the rest were sent.

        """
        await self._publish_entries(topic, [(p, _attributes(topic)) for p in payloads])

    async def publish_batch_keyed(
        self, topic: str, items: list[tuple[str, bytes]]
    ) -> None:
        """Send ``(key, payload)`` items like ``publish_batch``.

        Each with its key attribute.

        Raises:
            UnavailableError: Some entries were rejected twice; the rest were sent.

        """
        await self._publish_entries(
            topic, [(p, _attributes(topic, key)) for key, p in items]
        )

    @property
    def ordered_per_key(self) -> bool:
        """``False``: a standard SQS queue does not order messages by key."""
        return False

    async def _send_one(
        self,
        topic: str,
        payload: bytes,
        attributes: dict[str, MessageAttributeValueTypeDef],
    ) -> None:
        """Send one message with ``attributes`` to ``topic``'s queue.

        Raises:
            AppValueError: ``payload`` is too large, or ``topic`` has no queue.

        """
        check_payload_size(payload, self._max_payload_bytes)
        await self._require_client().send_message(
            QueueUrl=self._config.queue_url_for(topic),
            MessageBody=payload.decode("utf-8"),
            MessageAttributes=attributes,
        )

    async def _publish_entries(
        self,
        topic: str,
        messages: list[tuple[bytes, dict[str, MessageAttributeValueTypeDef]]],
    ) -> None:
        """Send ``(payload, attributes)`` messages to ``topic``'s queue ten per call.

        Bounded in flight.

        Every payload is size-checked before the first send, so an oversized one sends
        nothing.

        Raises:
            AppValueError: A payload is too large, or ``topic`` has no queue.
            UnavailableError: Some entries were rejected twice; the rest were sent.

        """
        self._require_client()
        for payload, _ in messages:
            check_payload_size(payload, self._max_payload_bytes)
        queue_url = self._config.queue_url_for(topic)
        entries: list[SendMessageBatchRequestEntryTypeDef] = [
            {"Id": str(i), "MessageBody": p.decode("utf-8"), "MessageAttributes": attrs}
            for i, (p, attrs) in enumerate(messages)
        ]
        # Batches go out concurrently (a burst costs about one round trip, not one per
        # batch), but at most publish_max_in_flight at once, so a large burst cannot
        # exhaust the connection pool.
        gate = asyncio.Semaphore(self._config.publish_max_in_flight)
        rejected = await asyncio.gather(
            *(
                self._send_batch(queue_url, entries[i : i + _SQS_BATCH_LIMIT], gate)
                for i in range(0, len(entries), _SQS_BATCH_LIMIT)
            )
        )
        lost = sum(rejected)
        if lost:
            msg = f"SQS rejected {lost} of {len(entries)} messages twice"
            raise UnavailableError(msg)

    async def _send_batch(
        self,
        queue_url: str,
        entries: list[SendMessageBatchRequestEntryTypeDef],
        gate: asyncio.Semaphore,
    ) -> int:
        """Send one batch, resend its rejected entries once.

        And return how many were rejected twice.
        """
        async with gate:
            failed = await self._send(queue_url, entries)
            if failed:
                by_id = {entry["Id"]: entry for entry in entries}
                failed = await self._send(
                    queue_url, [by_id[entry_id] for entry_id in failed]
                )
        return len(failed)

    async def _send(
        self, queue_url: str, entries: list[SendMessageBatchRequestEntryTypeDef]
    ) -> list[str]:
        """Send ``entries`` to ``queue_url`` in one call.

        And return the ids SQS rejected.
        """
        resp = await self._require_client().send_message_batch(
            QueueUrl=queue_url, Entries=entries
        )
        return [failed["Id"] for failed in resp.get("Failed", [])]

    def _require_client(self) -> SQSClient:
        """Return the open SQS client.

        Raises:
            AppRuntimeError: The publisher is not open.

        """
        if self._client is None:
            msg = "SQSPublisher not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        return self._client


def _attributes(
    topic: str, key: str | None = None
) -> dict[str, MessageAttributeValueTypeDef]:
    """Return a message's string attributes: its topic, its key if any.

    And the trace context.
    """
    carrier: dict[str, str] = {}
    inject(carrier)
    carrier["topic"] = topic
    if key is not None:
        carrier[PARTITION_KEY_ATTRIBUTE] = key
    return {k: {"DataType": "String", "StringValue": v} for k, v in carrier.items()}
