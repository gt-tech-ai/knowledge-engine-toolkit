"""SQS message consumer using aiobotocore long-polling.

Each handler call continues the trace the publisher put in the message's attributes and runs
through the handler tier. A handled message is settled (deleted) on its own, or, with
``SQSConfig.settle_batch``, together with the rest of its receive batch in one
``DeleteMessageBatch`` call; a delete SQS rejects is retried once on its own and then only logged,
since the message simply reappears after its visibility timeout.

The broker-neutral ``DeliveryConfig`` maps onto SQS here: ``retry_backoff`` sets a failed
message's visibility timeout from its receive count, ``max_deliveries`` / ``dead_letter_topic`` /
``max_processing_seconds`` are checked against the queue's redrive policy and visibility timeout
when a topic is subscribed (``GetQueueAttributes``, only when one is set), and ``extend`` (the
``LeaseExtender`` capability) is ``ChangeMessageVisibility`` on a message still being handled.
``backlog`` (the ``BacklogProbe`` capability) reads ``ApproximateNumberOfMessages``; SQS keeps
the oldest message's age only as a CloudWatch metric, so the age is reported as unknown.
"""

from __future__ import annotations

import asyncio
import logging
import math
from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.clients.messaging.config import DeliveryConfig
from techai_webutils.clients.messaging.handling import run_handler
from techai_webutils.clients.messaging.sqs.sqs_checks import check_queue
from techai_webutils.core.errors import AppRuntimeError, AppValueError, UnavailableError
from techai_webutils.core.interfaces.messaging import (
    LeaseExtender,
    Message,
    MessageConsumer,
    MessageHandler,
)
from techai_webutils.core.interfaces.messaging_backlog import Backlog, BacklogProbe

if TYPE_CHECKING:
    from collections.abc import Sequence
    from types import TracebackType

    from aiobotocore.session import AioSession
    from types_aiobotocore_sqs import SQSClient
    from types_aiobotocore_sqs.type_defs import (
        DeleteMessageBatchRequestEntryTypeDef,
        MessageTypeDef,
        ReceiveMessageRequestTypeDef,
    )

    from techai_webutils.clients.client import ClientBase
    from techai_webutils.clients.messaging.config import SQSConfig

logger = logging.getLogger(__name__)

_SQS_BATCH_LIMIT = 10
"""Most entries one SQS batch call accepts."""

_MAX_VISIBILITY_SECONDS = 43_200
"""The longest visibility timeout SQS accepts (12 hours)."""


class SQSSubscriber(MessageConsumer, LeaseExtender, BacklogProbe):
    """Consumes messages from an SQS queue via long-polling.

    Usage::

        async with SQSSubscriber(config) as sub:
            await sub.subscribe("events", handler)
    """

    def __init__(
        self,
        config: SQSConfig,
        handlers: ClientBase | None = None,
        *,
        delivery: DeliveryConfig | None = None,
    ) -> None:
        """Store the SQS connection config; the client is created on context entry.

        ``handlers`` is the handler tier (``ClientBase.wire`` with ``handler()``) each handler call
        runs through; None runs handlers undecorated. ``delivery`` holds the retry, dead-letter and
        processing-time settings (None: all unset, today's behaviour).
        """
        self._config = config
        self._handlers = handlers
        self._delivery = delivery or DeliveryConfig()
        self._client: SQSClient | None = None
        self._session: AioSession | None = None
        self._running = False
        self._leases: dict[str, tuple[str, str]] = {}

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
        """Stop the consumer loop and close the aiobotocore SQS client on exit."""
        self._running = False
        if self._client is not None:
            await self._client.__aexit__(exc_type, exc_val, exc_tb)

    async def subscribe(self, topic: str, handler: MessageHandler) -> None:
        """Start consuming ``topic``'s queue. Blocks until ``close()`` is called.

        Raises:
            AppRuntimeError: The subscriber is not open.
            AppValueError: ``topic`` has no queue, or the queue's settings disagree with the
                delivery config.
            UnavailableError: The queue-settings check timed out.

        """
        client = self._require_client()
        queue_url = self._config.queue_url_for(topic)
        await check_queue(
            client, queue_url, self._delivery, self._config.attributes_timeout_seconds
        )
        request = self._receive_request(queue_url)
        self._running = True
        while self._running:
            resp = await client.receive_message(**request)
            messages = resp.get("Messages", [])
            if messages:
                # Fan the poll batch out concurrently instead of awaiting each message serially, so a
                # consumer built on this reference subscriber gets batch throughput. Each message still
                # acks (deletes) only on its own handler success; a failing handler is logged and left
                # for redrive. No explicit semaphore is needed — SQS caps a receive batch at 10
                # (max_messages), which is already a safe fan-out width, so the batch size IS the bound.
                handled = await asyncio.gather(
                    *(
                        self._handle_one(topic, queue_url, raw, handler)
                        for raw in messages
                    )
                )
                if self._config.settle_batch:
                    await self._settle(queue_url, [h for h in handled if h is not None])

    def _receive_request(self, queue_url: str) -> ReceiveMessageRequestTypeDef:
        """Build the receive call's arguments; the receive count is asked for only for backoff."""
        request: ReceiveMessageRequestTypeDef = {
            "QueueUrl": queue_url,
            "MaxNumberOfMessages": self._config.max_messages,
            "WaitTimeSeconds": self._config.wait_time_seconds,
            "MessageAttributeNames": ["All"],
        }
        if self._delivery.retry_backoff is not None:
            request["MessageSystemAttributeNames"] = ["ApproximateReceiveCount"]
        return request

    async def _handle_one(
        self, topic: str, queue_url: str, raw: MessageTypeDef, handler: MessageHandler
    ) -> str | None:
        """Deliver one message to the handler and settle it on success (log + leave for redrive on error).

        With ``settle_batch`` the message is not deleted here: its receipt handle is returned for
        ``_settle``; otherwise it is deleted at once and ``None`` is returned, as is the handle of
        a message whose handler failed. A failed message is retried after its backoff when
        ``retry_backoff`` is set.

        SQS always returns ``MessageId``, ``Body`` and ``ReceiptHandle`` on a received message; the
        SDK types mark them optional, so they are read with ``.get``. A message with no
        ``ReceiptHandle`` cannot be deleted, so it is logged and skipped rather than handled:
        handling it would only repeat the work on every redelivery.
        """
        receipt_handle = raw.get("ReceiptHandle")
        if not receipt_handle:
            logger.error(
                "Skipping SQS message %s: it has no ReceiptHandle and cannot be acknowledged",
                raw.get("MessageId", ""),
            )
            return None
        msg_obj = Message(
            id=raw.get("MessageId", ""),
            topic=topic,
            payload=raw.get("Body", "").encode("utf-8"),
            metadata=self._extract_metadata(raw),
            delivery=int(raw.get("Attributes", {}).get("ApproximateReceiveCount", "1")),
        )
        self._leases[msg_obj.id] = (queue_url, receipt_handle)
        try:
            await run_handler(self._handlers, topic, handler, msg_obj)
            if self._config.settle_batch:
                return receipt_handle
            await self._require_client().delete_message(
                QueueUrl=queue_url,
                ReceiptHandle=receipt_handle,
            )
        except Exception:
            logger.exception("Failed to process message %s", msg_obj.id)
            await self._back_off(queue_url, raw, receipt_handle)
        finally:
            self._leases.pop(msg_obj.id, None)
        return None

    async def _back_off(
        self, queue_url: str, raw: MessageTypeDef, receipt_handle: str
    ) -> None:
        """Hide a failed message for its ``retry_backoff`` delay; never raises.

        The delay is derived from the message's ``ApproximateReceiveCount``. Without a backoff the
        message reappears after the queue's own visibility timeout, as before.
        """
        delivery = int(raw.get("Attributes", {}).get("ApproximateReceiveCount", "1"))
        delay = self._delivery.backoff_seconds(delivery)
        if delay is None:
            return
        try:
            await self._require_client().change_message_visibility(
                QueueUrl=queue_url,
                ReceiptHandle=receipt_handle,
                VisibilityTimeout=min(math.ceil(delay), _MAX_VISIBILITY_SECONDS),
            )
        except Exception:
            logger.exception(
                "SQS backoff of a failed message failed; it reappears on schedule"
            )

    async def extend(self, message: Message, seconds: float) -> None:
        """Keep ``message`` hidden from other consumers for ``seconds`` more from now.

        Raises:
            AppValueError: ``message`` is not being handled by this subscriber.

        """
        lease = self._leases.get(message.id)
        if lease is None:
            msg = f"message {message.id} is not being handled by this subscriber"
            raise AppValueError(msg)
        queue_url, receipt_handle = lease
        await self._require_client().change_message_visibility(
            QueueUrl=queue_url,
            ReceiptHandle=receipt_handle,
            VisibilityTimeout=min(math.ceil(seconds), _MAX_VISIBILITY_SECONDS),
        )

    async def backlog(self, topic: str) -> Backlog:
        """Return the messages waiting on ``topic``'s queue; the age is unknown on SQS.

        Raises:
            AppValueError: ``topic`` has no queue.
            UnavailableError: ``GetQueueAttributes`` did not answer within
                ``attributes_timeout_seconds``.

        """
        queue_url = self._config.queue_url_for(topic)
        try:
            async with asyncio.timeout(self._config.attributes_timeout_seconds):
                resp = await self._require_client().get_queue_attributes(
                    QueueUrl=queue_url, AttributeNames=["ApproximateNumberOfMessages"]
                )
        except TimeoutError as exc:
            msg = f"GetQueueAttributes for {queue_url} timed out"
            raise UnavailableError(msg) from exc
        waiting = resp.get("Attributes", {}).get("ApproximateNumberOfMessages", "0")
        return Backlog(messages=int(waiting), oldest_age_seconds=None)

    async def _settle(self, queue_url: str, receipt_handles: Sequence[str]) -> None:
        """Delete handled messages ``_SQS_BATCH_LIMIT`` at a time; never raises.

        An entry SQS rejects is deleted once more on its own; a failure of that, or of a whole
        batch call, is logged and the message reappears after its visibility timeout.
        """
        for start in range(0, len(receipt_handles), _SQS_BATCH_LIMIT):
            chunk = receipt_handles[start : start + _SQS_BATCH_LIMIT]
            entries: list[DeleteMessageBatchRequestEntryTypeDef] = [
                {"Id": str(i), "ReceiptHandle": h} for i, h in enumerate(chunk)
            ]
            try:
                resp = await self._require_client().delete_message_batch(
                    QueueUrl=queue_url, Entries=entries
                )
            except Exception:
                logger.exception("SQS batch delete of %d messages failed", len(chunk))
                continue
            for failed in resp.get("Failed", []):
                await self._delete_alone(
                    queue_url, chunk[int(failed["Id"])], failed.get("Code", "")
                )

    async def _delete_alone(self, queue_url: str, receipt_handle: str, code: str) -> None:
        """Retry one rejected batch delete on its own, logging (never raising) a second failure."""
        try:
            await self._require_client().delete_message(
                QueueUrl=queue_url, ReceiptHandle=receipt_handle
            )
        except Exception:
            logger.exception(
                "SQS delete failed twice (batch code %s); left for redrive", code
            )

    def _require_client(self) -> SQSClient:
        """Return the open SQS client.

        Raises:
            AppRuntimeError: The subscriber is not open.

        """
        if self._client is None:
            msg = "SQSSubscriber not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        return self._client

    async def close(self) -> None:
        """Stop the consumer loop gracefully."""
        self._running = False

    @staticmethod
    def _extract_metadata(raw: MessageTypeDef) -> dict[str, str]:
        """Extract string attributes from an SQS message."""
        attrs = raw.get("MessageAttributes", {})
        return {
            k: v.get("StringValue", "")
            for k, v in attrs.items()
            if v.get("DataType") == "String"
        }
