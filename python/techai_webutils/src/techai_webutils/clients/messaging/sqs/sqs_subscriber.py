"""SQS message consumer using aiobotocore long-polling.

Each handler call continues the trace the publisher put in the message's attributes and runs
through the handler tier. A handled message is settled (deleted) on its own, or, with
``SQSConfig.settle_batch``, together with the rest of its receive batch in one
``DeleteMessageBatch`` call; a delete SQS rejects is retried once on its own and then only logged,
since the message simply reappears after its visibility timeout.
"""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, Self

import aiobotocore.session

from techai_webutils.clients.messaging.handling import run_handler
from techai_webutils.core.errors import AppRuntimeError
from techai_webutils.core.interfaces.messaging import (
    Message,
    MessageConsumer,
    MessageHandler,
)

if TYPE_CHECKING:
    from collections.abc import Sequence
    from types import TracebackType

    from aiobotocore.session import AioSession
    from types_aiobotocore_sqs import SQSClient
    from types_aiobotocore_sqs.type_defs import (
        DeleteMessageBatchRequestEntryTypeDef,
        MessageTypeDef,
    )

    from techai_webutils.clients.client import ClientBase
    from techai_webutils.clients.messaging.config import SQSConfig

logger = logging.getLogger(__name__)

_SQS_BATCH_LIMIT = 10
"""Most entries one SQS batch call accepts."""


class SQSSubscriber(MessageConsumer):
    """Consumes messages from an SQS queue via long-polling.

    Usage::

        async with SQSSubscriber(config) as sub:
            await sub.subscribe("events", handler)
    """

    def __init__(self, config: SQSConfig, handlers: ClientBase | None = None) -> None:
        """Store the SQS connection config; the client is created on context entry.

        ``handlers`` is the handler tier (``ClientBase.wire`` with ``handler()``) each handler call runs through;
        None runs handlers undecorated.
        """
        self._config = config
        self._handlers = handlers
        self._client: SQSClient | None = None
        self._session: AioSession | None = None
        self._running = False

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
        """Start consuming messages. Blocks until ``close()`` is called."""
        if self._client is None:
            msg = "SQSSubscriber not initialized. Use as async context manager."
            raise AppRuntimeError(msg)
        self._running = True
        while self._running:
            resp = await self._client.receive_message(
                QueueUrl=self._config.queue_url,
                MaxNumberOfMessages=self._config.max_messages,
                WaitTimeSeconds=self._config.wait_time_seconds,
                MessageAttributeNames=["All"],
            )
            messages = resp.get("Messages", [])
            if messages:
                # Fan the poll batch out concurrently instead of awaiting each message serially, so a
                # consumer built on this reference subscriber gets batch throughput. Each message still
                # acks (deletes) only on its own handler success; a failing handler is logged and left
                # for redrive. No explicit semaphore is needed — SQS caps a receive batch at 10
                # (max_messages), which is already a safe fan-out width, so the batch size IS the bound.
                handled = await asyncio.gather(
                    *(self._handle_one(topic, raw, handler) for raw in messages)
                )
                if self._config.settle_batch:
                    await self._settle([h for h in handled if h is not None])

    async def _handle_one(
        self, topic: str, raw: MessageTypeDef, handler: MessageHandler
    ) -> str | None:
        """Deliver one message to the handler and settle it on success (log + leave for redrive on error).

        With ``settle_batch`` the message is not deleted here: its receipt handle is returned for
        ``_settle``; otherwise it is deleted at once and ``None`` is returned, as is the handle of
        a message whose handler failed.

        SQS always returns ``MessageId``, ``Body`` and ``ReceiptHandle`` on a received message; the
        SDK types mark them optional, so they are read with ``.get``. A message with no
        ``ReceiptHandle`` cannot be deleted, so it is logged and skipped rather than handled:
        handling it would only repeat the work on every redelivery.
        """
        if self._client is None:
            return None
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
        )
        try:
            await run_handler(self._handlers, topic, handler, msg_obj)
            if self._config.settle_batch:
                return receipt_handle
            await self._client.delete_message(
                QueueUrl=self._config.queue_url,
                ReceiptHandle=receipt_handle,
            )
        except Exception:
            logger.exception("Failed to process message %s", msg_obj.id)
        return None

    async def _settle(self, receipt_handles: Sequence[str]) -> None:
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
                    QueueUrl=self._config.queue_url, Entries=entries
                )
            except Exception:
                logger.exception("SQS batch delete of %d messages failed", len(chunk))
                continue
            for failed in resp.get("Failed", []):
                await self._delete_alone(chunk[int(failed["Id"])], failed.get("Code", ""))

    async def _delete_alone(self, receipt_handle: str, code: str) -> None:
        """Retry one rejected batch delete on its own, logging (never raising) a second failure."""
        try:
            await self._require_client().delete_message(
                QueueUrl=self._config.queue_url, ReceiptHandle=receipt_handle
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
