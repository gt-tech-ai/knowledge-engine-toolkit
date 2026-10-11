"""Tests for SQS publisher with mocked aiobotocore."""

from __future__ import annotations

from contextlib import AsyncExitStack
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig


async def _open(config: SQSConfig) -> SQSPublisher:
    """Open a publisher on the ``aws_client`` session; it stays open, as the session is a mock."""
    return await AsyncExitStack().enter_async_context(SQSPublisher(config))


class TestSQSPublisher:
    """Test suite for SQSPublisher message sending and lifecycle."""

    @pytest.mark.asyncio
    async def test_publish_sends_message(
        self, sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that publish sends a single message to the configured SQS queue.

        **Why this test is important:**
          - Message publishing is the primary mechanism for async event-driven communication
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
        mock_client.send_message_batch = AsyncMock()

        publisher = await _open(sqs_config)

        await publisher.publish_batch("events", [b"msg1", b"msg2"])

        mock_client.send_message_batch.assert_called_once()
        call_kwargs = mock_client.send_message_batch.call_args[1]
        assert len(call_kwargs["Entries"]) == 2

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
          - Consistent error behavior across publish and publish_batch simplifies error handling

        **What it tests:**
          - RuntimeError with "not initialized" message is raised
        """
        publisher = SQSPublisher(sqs_config)
        with pytest.raises(RuntimeError, match="not initialized"):
            await publisher.publish_batch("topic", [b"data"])
