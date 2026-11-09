"""Tests for SQS publisher with mocked aiobotocore."""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
from dataclasses import replace
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.core.errors import UnavailableError

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig


async def _open(config: SQSConfig) -> SQSPublisher:
    """Open a publisher on the ``aws_client`` session; it stays open.

    As the session is a mock.
    """
    return await AsyncExitStack().enter_async_context(SQSPublisher(config))


class TestSQSPublisher:
    """Test suite for SQSPublisher message sending and lifecycle."""

    @pytest.mark.asyncio
    async def test_publish_sends_message(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that publish sends a single message to the configured SQS queue.

        **Why this test is important:**
          - Message publishing is the primary mechanism for async event-driven
            communication
          - Incorrect queue URL or message body would route events to wrong consumers
          - The ingestion pipeline depends on correct SQS message delivery

        **What it tests:**
          - send_message is called exactly once
          - QueueUrl matches the configured queue URL
          - MessageBody matches the published payload
        """
        mock_client = aws_client
        mock_client.send_message = AsyncMock()

        publisher = await _open(sqs_config)

        await publisher.publish("events", b'{"type":"test"}')

        mock_client.send_message.assert_called_once()
        call_kwargs = mock_client.send_message.call_args[1]
        assert call_kwargs["QueueUrl"] == sqs_config.queue_url
        assert call_kwargs["MessageBody"] == '{"type":"test"}'

    @pytest.mark.asyncio
    async def test_publish_batch_sends_messages(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that publish_batch sends multiple messages in a single SQS batch request.

        **Why this test is important:**
          - Batch publishing reduces SQS API calls and cost for high-throughput ingestion
          - Each message in the batch must be included as a separate entry
          - Incorrect batching would lose messages or cause partial delivery

        **What it tests:**
          - send_message_batch is called exactly once
          - Entries list contains exactly 2 messages
        """
        mock_client = aws_client
        mock_client.send_message_batch = AsyncMock(
            return_value={"Successful": [{"Id": "0"}, {"Id": "1"}], "Failed": []}
        )

        publisher = await _open(sqs_config)

        await publisher.publish_batch("events", [b"msg1", b"msg2"])

        mock_client.send_message_batch.assert_called_once()
        call_kwargs = mock_client.send_message_batch.call_args[1]
        assert len(call_kwargs["Entries"]) == 2

    @pytest.mark.asyncio
    @pytest.mark.parametrize("max_in_flight", [8, 3])
    async def test_publish_batch_bounds_concurrency(
        self, sqs_config: SQSConfig, aws_client: AsyncMock, max_in_flight: int
    ) -> None:
        """``publish_batch`` keeps at most ``publish_max_in_flight`` batch sends.

        In flight.

        **Why this test is important:**
          - An unbounded fan-out of a large burst opens hundreds of concurrent requests,
            which exhausts the connection pool and gets throttled.

        **What it tests:**
          - 100 messages (10 batches of 10) never have more than ``publish_max_in_flight``
            ``send_message_batch`` calls running at once, and all 100 are sent.
        """
        in_flight = 0
        peak = 0
        sent = 0

        async def send(**kwargs: object) -> dict[str, object]:
            nonlocal in_flight, peak, sent
            in_flight += 1
            peak = max(peak, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            entries = cast("list[object]", kwargs["Entries"])
            sent += len(entries)
            return {"Successful": [], "Failed": []}

        aws_client.send_message_batch.side_effect = send
        publisher = await _open(replace(sqs_config, publish_max_in_flight=max_in_flight))

        await publisher.publish_batch("events", [b"m"] * 100)

        assert peak == max_in_flight
        assert sent == 100

    @pytest.mark.asyncio
    async def test_publish_batch_retries_failed_entries_once_then_raises(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Entries a batch send rejects are resent once.

        Entries rejected again raise a coded error.

        **Why this test is important:**
          - ``SendMessageBatch`` succeeds as a call while failing some entries; ignoring
            its ``Failed`` list loses those messages without a trace.

        **What it tests:**
          - A first response failing entry ``1`` is followed by one resend of exactly that
            entry; when the resend fails it too, ``publish_batch`` raises
            ``UnavailableError``. A resend that succeeds returns normally.
        """
        failed = {
            "Successful": [{"Id": "0"}],
            "Failed": [{"Id": "1", "Code": "InternalError"}],
        }
        aws_client.send_message_batch.side_effect = [
            failed,
            {"Successful": [], "Failed": [{"Id": "1", "Code": "InternalError"}]},
        ]
        publisher = await _open(sqs_config)

        with pytest.raises(UnavailableError, match="1 of 2"):
            await publisher.publish_batch("events", [b"a", b"b"])
        resend = aws_client.send_message_batch.await_args_list[1].kwargs["Entries"]
        assert [e["MessageBody"] for e in resend] == ["b"]

        aws_client.send_message_batch.side_effect = [
            failed,
            {"Successful": [{"Id": "1"}], "Failed": []},
        ]
        await publisher.publish_batch("events", [b"a", b"b"])

    @pytest.mark.asyncio
    async def test_publish_raises_when_not_initialized(
        self, sqs_config: SQSConfig
    ) -> None:
        """Test that publish raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Using the publisher before async initialization would send to a None client
          - Fail-fast with a clear error prevents silent message loss
          - Service startup ordering bugs are caught immediately with this guard

        **What it tests:**
          - RuntimeError with "not initialized" message is raised
        """
        publisher = SQSPublisher(sqs_config)
        with pytest.raises(RuntimeError, match="not initialized"):
            await publisher.publish("topic", b"data")

    @pytest.mark.asyncio
    async def test_publish_batch_raises_when_not_initialized(
        self, sqs_config: SQSConfig
    ) -> None:
        """Test that publish_batch raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Batch operations share the same initialization guard as single publish
          - Uninitialized batch calls would silently lose multiple messages at once
          - Consistent error behavior across publish and publish_batch simplifies error
            handling

        **What it tests:**
          - RuntimeError with "not initialized" message is raised
        """
        publisher = SQSPublisher(sqs_config)
        with pytest.raises(RuntimeError, match="not initialized"):
            await publisher.publish_batch("topic", [b"data"])
