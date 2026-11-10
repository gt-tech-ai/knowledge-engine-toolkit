"""Tests for SQS subscriber with mocked aiobotocore."""

from __future__ import annotations

import asyncio
from dataclasses import replace
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.errors import AppRuntimeError

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message, MessageHandler

_EXPECTED_AWAIT_COUNT = 2
_EXPECTED_AWAIT_COUNT_23 = 23


async def _metadata_of(
    config: SQSConfig, aws_client: AsyncMock, raw: dict[str, object]
) -> dict[str, str]:
    """Deliver ``raw`` through ``subscribe`` once.

    Return the metadata the handler received.

    Returns:
        The delivered message's metadata.

    """
    cast("MagicMock", aws_client.receive_message).return_value = {"Messages": [raw]}
    received: list[Message] = []
    async with SQSSubscriber(config) as subscriber:

        async def handler(msg: Message) -> None:
            received.append(msg)
            await subscriber.close()

        await subscriber.subscribe("events", handler)
    [message] = received
    return message.metadata


class TestSQSSubscriber:
    """Test suite for SQSSubscriber lifecycle and metadata extraction."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_subscribe_raises_when_not_initialized(sqs_config: SQSConfig) -> None:
        """Test that subscribe raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Subscribing without initialization would poll with a None client
          - Fail-fast prevents silent consumption failures and missed messages
          - Service startup ordering bugs are caught immediately

        **What it tests:**
          - RuntimeError with "not initialized" message is raised
        """
        sub = SQSSubscriber(sqs_config)

        async def handler(_msg: Message) -> None:
            pass

        with pytest.raises(AppRuntimeError, match="not initialized"):
            await sub.subscribe("topic", handler)

    @staticmethod
    @pytest.mark.asyncio
    async def test_close_stops_the_polling_loop(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that close stops a running subscribe loop after the current poll.

        **Why this test is important:**
          - Graceful shutdown requires the polling loop to stop cleanly
          - Kubernetes sends SIGTERM before killing pods; the subscriber must stop polling
          - Failure to close would cause the pod to be force-killed and lose in-flight
            messages

        **What it tests:**
          - close() during a poll makes subscribe() return after exactly one receive
        """
        async with SQSSubscriber(sqs_config) as subscriber:

            async def receive(**_kwargs: object) -> dict[str, object]:
                await subscriber.close()
                return {"Messages": []}

            cast("MagicMock", aws_client.receive_message).side_effect = receive

            async def handler(_msg: Message) -> None:
                pass

            await subscriber.subscribe("topic", handler)

        cast(
            "MagicMock", cast("AsyncMock", aws_client.receive_message).assert_awaited_once
        )()

    @staticmethod
    @pytest.mark.asyncio
    async def test_extract_metadata(sqs_config: SQSConfig, aws_client: AsyncMock) -> None:
        """Test that a delivered message carries its SQS MessageAttributes as a flat dict.

        **Why this test is important:**
          - Message metadata carries routing information (topic, version) for consumers
          - Incorrect parsing would misroute messages or lose versioning context
          - The flat dict format is expected by all downstream message handlers

        **What it tests:**
          - Metadata dict contains "topic" with value "events"
          - Metadata dict contains "version" with value "1"
        """
        raw: dict[str, object] = {
            "MessageId": "m-1",
            "Body": "{}",
            "ReceiptHandle": "r-1",
            "MessageAttributes": {
                "topic": {"DataType": "String", "StringValue": "events"},
                "version": {"DataType": "String", "StringValue": "1"},
            },
        }
        metadata = await _metadata_of(sqs_config, aws_client, raw)
        assert metadata == {"topic": "events", "version": "1"}

    @staticmethod
    @pytest.mark.asyncio
    async def test_extract_metadata_empty(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that a message with no attributes is delivered with empty metadata.

        **Why this test is important:**
          - Messages without attributes are valid in SQS and must not cause KeyError
          - The empty dict allows callers to safely iterate or check metadata
          - This edge case occurs with legacy publishers that do not set attributes

        **What it tests:**
          - Result equals an empty dict for a message with no MessageAttributes
        """
        raw: dict[str, object] = {
            "MessageId": "m-1",
            "Body": "{}",
            "ReceiptHandle": "r-1",
        }
        assert await _metadata_of(sqs_config, aws_client, raw) == {}

    @staticmethod
    @pytest.mark.asyncio
    async def test_message_without_receipt_handle_is_not_handled(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that a received message with no ReceiptHandle is skipped.

        And polling goes on.

        **Why this test is important:**
          - A message that cannot be deleted must not be processed: its handler would run,
            the delete would fail on an empty handle, and the message would be redelivered
            and handled again.
          - One malformed message must not stop the subscriber for the rest of the queue.

        **What it tests:**
          - The handler is never called and delete_message is never awaited.
          - subscribe keeps polling: a second receive happens after the bad message.
        """
        handler = AsyncMock()
        async with SQSSubscriber(sqs_config) as subscriber:
            polls = 0

            async def receive(**_kwargs: object) -> dict[str, object]:
                nonlocal polls
                polls += 1
                if polls == 1:
                    return {"Messages": [{"MessageId": "m-1", "Body": "{}"}]}
                await subscriber.close()
                return {"Messages": []}

            cast("MagicMock", aws_client.receive_message).side_effect = receive
            await subscriber.subscribe("events", handler)

        handler.assert_not_awaited()
        cast(
            "MagicMock", cast("AsyncMock", aws_client.delete_message).assert_not_awaited
        )()
        assert (
            cast("AsyncMock", aws_client.receive_message).await_count
        ) == _EXPECTED_AWAIT_COUNT


def _raw(n: int) -> dict[str, object]:
    """Return received SQS message ``n`` with its receipt handle.

    Returns:
        Received SQS message ``n`` with its receipt handle.

    """
    return {"MessageId": f"m-{n}", "Body": "{}", "ReceiptHandle": f"r-{n}"}


async def _consume(
    config: SQSConfig,
    aws_client: AsyncMock,
    polls: list[list[dict[str, object]]],
    handler: MessageHandler,
) -> None:
    """Run ``subscribe`` over the given receive batches, closing after the last."""
    batches = iter(polls)
    async with SQSSubscriber(config) as subscriber:

        async def receive(**_kwargs: object) -> dict[str, object]:
            batch = next(batches, None)
            if batch is None:
                await subscriber.close()
                return {"Messages": []}
            return {"Messages": batch}

        cast("MagicMock", aws_client.receive_message).side_effect = receive
        await subscriber.subscribe("events", handler)


class TestSQSSubscriberSettleBatch:
    """``settle_batch``: settled messages are deleted with ``DeleteMessageBatch``."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_settled_messages_are_deleted_in_batches(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """23 settled messages over three receives are deleted in three batch calls.

        **Why this test is important:**
          - One ``DeleteMessage`` per message doubles the SQS request count of a busy
            consumer (one receive per ten messages, then ten deletes); batching cuts the
            deletes tenfold.

        **What it tests:**
          - With ``settle_batch=True`` and receives of 10, 10 and 3 messages, every
            message is handled, ``delete_message_batch`` runs three times with 10, 10 and
            3 receipt handles, and ``delete_message`` never runs.
        """
        cast("MagicMock", aws_client.delete_message_batch).return_value = {
            "Successful": [],
            "Failed": [],
        }
        polls = [
            [_raw(n) for n in range(start, end)]
            for start, end in ((0, 10), (10, 20), (20, 23))
        ]
        handler = AsyncMock()

        await _consume(replace(sqs_config, settle_batch=True), aws_client, polls, handler)

        assert handler.await_count == _EXPECTED_AWAIT_COUNT_23
        sizes = [
            len(cast("list[object]", c.kwargs["Entries"]))
            for c in cast(
                "list[object]",
                cast("AsyncMock", aws_client.delete_message_batch).await_args_list,
            )
        ]
        assert sizes == [10, 10, 3]
        handles = {
            entry["ReceiptHandle"]
            for c in cast(
                "list[object]",
                cast("AsyncMock", aws_client.delete_message_batch).await_args_list,
            )
            for entry in cast("list[dict[str, object]]", c.kwargs["Entries"])
        }
        assert handles == {f"r-{n}" for n in range(23)}
        cast(
            "MagicMock", cast("AsyncMock", aws_client.delete_message).assert_not_awaited
        )()

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_failed_handler_leaves_its_message_for_redrive(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A message whose handler raised is left out of the batch delete.

        **Why this test is important:**
          - Deleting a message whose handling failed would lose it; it must reappear after
            its visibility timeout, as on the single-delete path.

        **What it tests:**
          - Of three messages, the one whose handler raises is not in the batch delete.
        """
        cast("MagicMock", aws_client.delete_message_batch).return_value = {
            "Successful": [],
            "Failed": [],
        }

        async def handler(message: Message) -> None:
            await asyncio.sleep(0)
            if message.id == "m-1":
                msg = "boom"
                raise AppRuntimeError(msg)

        await _consume(
            replace(sqs_config, settle_batch=True),
            aws_client,
            [[_raw(0), _raw(1), _raw(2)]],
            handler,
        )

        [entries] = [
            cast("dict[str, object]", c.kwargs)["Entries"]
            for c in cast(
                "list[object]",
                cast("AsyncMock", aws_client.delete_message_batch).await_args_list,
            )
        ]
        assert {e["ReceiptHandle"] for e in cast("list[dict[str, object]]", entries)} == {
            "r-0",
            "r-2",
        }

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_failed_entry_is_retried_once_and_never_raised(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A batch entry SQS rejects is deleted once on its own.

        A second failure is only logged.

        **Why this test is important:**
          - A rejected delete only means the message reappears and is handled again;
            raising would stop the consumer for a failure that redrive already covers.

        **What it tests:**
          - With entry ``1`` failed in the batch response, ``delete_message`` runs once
            for its receipt handle; when that also raises, ``subscribe`` still returns
            normally.
        """
        cast("MagicMock", aws_client.delete_message_batch).return_value = {
            "Successful": [{"Id": "0"}],
            "Failed": [
                {"Id": "1", "Code": "ReceiptHandleIsInvalid", "SenderFault": True}
            ],
        }
        cast("MagicMock", aws_client.delete_message).side_effect = AppRuntimeError(
            "still failing"
        )

        await _consume(
            replace(sqs_config, settle_batch=True),
            aws_client,
            [[_raw(0), _raw(1)]],
            AsyncMock(),
        )

        cast(
            "MagicMock",
            cast("AsyncMock", aws_client.delete_message).assert_awaited_once_with,
        )(QueueUrl=sqs_config.queue_url, ReceiptHandle="r-1")

    @staticmethod
    @pytest.mark.asyncio
    async def test_without_settle_batch_each_message_is_deleted_alone(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """The default keeps today's one ``DeleteMessage`` per handled message.

        **Why this test is important:**
          - Batching is opt-in; a consumer that has not enabled it must see no change.

        **What it tests:**
          - With the default config, two handled messages are deleted with two
            ``delete_message`` calls and no ``delete_message_batch``.
        """
        await _consume(sqs_config, aws_client, [[_raw(0), _raw(1)]], AsyncMock())

        assert (
            cast("AsyncMock", aws_client.delete_message).await_count
        ) == _EXPECTED_AWAIT_COUNT
        cast(
            "MagicMock",
            cast("AsyncMock", aws_client.delete_message_batch).assert_not_awaited,
        )()
