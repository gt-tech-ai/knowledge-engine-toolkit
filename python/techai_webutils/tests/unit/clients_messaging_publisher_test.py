"""Tests for SQS publisher with mocked aiobotocore."""

from __future__ import annotations

import asyncio
from contextlib import AsyncExitStack
from dataclasses import replace
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.core.errors import AppRuntimeError, UnavailableError
from tests.json_path import dig

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig

_EXPECTED_CALL_KWARGS_COUNT = 2
_EXPECTED_SENT = 100


async def _open(config: SQSConfig) -> SQSPublisher:
    """Open a publisher on the ``aws_client`` session.

    It stays open, as the session is a mock.

    Args:
        config: The queue's settings.

    Returns:
        A publisher on the ``aws_client`` session; it stays open.

    """
    return await AsyncExitStack().enter_async_context(SQSPublisher(config))


class TestSQSPublisher:
    """Test suite for SQSPublisher message sending and lifecycle."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_publish_sends_message(
        sqs_config: SQSConfig, aws_client: AsyncMock
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

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.
            aws_client: The client the patched aiobotocore session opens.

        """
        mock_client = aws_client
        mock_client.send_message = AsyncMock()

        publisher = await _open(sqs_config)

        await publisher.publish("events", b'{"type":"test"}')

        cast(
            "MagicMock", cast("MagicMock", mock_client.send_message).assert_called_once
        )()
        call_kwargs = cast(
            "dict[str, object]", cast("MagicMock", mock_client.send_message).call_args[1]
        )
        assert call_kwargs["QueueUrl"] == sqs_config.queue_url
        assert call_kwargs["MessageBody"] == '{"type":"test"}'

    @staticmethod
    @pytest.mark.asyncio
    async def test_publish_batch_sends_messages(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """Test that publish_batch sends multiple messages in a single SQS batch request.

        **Why this test is important:**
          - Batch publishing reduces SQS API calls and cost for high-throughput ingestion
          - Each message in the batch must be included as a separate entry
          - Incorrect batching would lose messages or cause partial delivery

        **What it tests:**
          - send_message_batch is called exactly once
          - Entries list contains exactly 2 messages

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.
            aws_client: The client the patched aiobotocore session opens.

        """
        mock_client = aws_client
        mock_client.send_message_batch = AsyncMock(
            return_value={"Successful": [{"Id": "0"}, {"Id": "1"}], "Failed": []}
        )

        publisher = await _open(sqs_config)

        await publisher.publish_batch("events", [b"msg1", b"msg2"])

        cast(
            "MagicMock",
            cast("MagicMock", mock_client.send_message_batch).assert_called_once,
        )()
        call_kwargs = cast(
            "dict[str, object]",
            cast("MagicMock", mock_client.send_message_batch).call_args[1],
        )
        assert (
            len(cast("list[object]", call_kwargs["Entries"]))
            == _EXPECTED_CALL_KWARGS_COUNT
        )

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize("max_in_flight", [8, 3])
    async def test_publish_batch_bounds_concurrency(
        sqs_config: SQSConfig, aws_client: AsyncMock, max_in_flight: int
    ) -> None:
        """``publish_batch`` bounds its batch sends in flight.

        At most ``publish_max_in_flight``.

        **Why this test is important:**
          - An unbounded fan-out of a large burst opens hundreds of concurrent requests,
            which exhausts the connection pool and gets throttled.

        **What it tests:**
          - 100 messages (10 batches of 10) never have more than ``publish_max_in_flight``
            ``send_message_batch`` calls running at once, and all 100 are sent.

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.
            aws_client: The client the patched aiobotocore session opens.
            max_in_flight: The most sends at once.

        """
        in_flight = 0
        peak = 0
        sent = 0

        async def send(**kwargs: object) -> dict[str, object]:
            """Accept a batch, tracking how many sends run at once.

            Args:
                **kwargs: The request, with its entries.

            Returns:
                A response accepting every entry.

            """
            nonlocal in_flight, peak, sent
            in_flight += 1
            peak = max(peak, in_flight)
            await asyncio.sleep(0.01)
            in_flight -= 1
            entries = cast("list[object]", kwargs["Entries"])
            sent += len(entries)
            return {"Successful": [], "Failed": []}

        cast("MagicMock", aws_client.send_message_batch).side_effect = send
        publisher = await _open(replace(sqs_config, publish_max_in_flight=max_in_flight))

        await publisher.publish_batch("events", [b"m"] * 100)

        assert peak == max_in_flight
        assert sent == _EXPECTED_SENT

    @staticmethod
    @pytest.mark.asyncio
    async def test_publish_batch_retries_failed_entries_once_then_raises(
        sqs_config: SQSConfig, aws_client: AsyncMock
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

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.
            aws_client: The client the patched aiobotocore session opens.

        """
        failed = {
            "Successful": [{"Id": "0"}],
            "Failed": [{"Id": "1", "Code": "InternalError"}],
        }
        cast("MagicMock", aws_client.send_message_batch).side_effect = [
            failed,
            {"Successful": [], "Failed": [{"Id": "1", "Code": "InternalError"}]},
        ]
        publisher = await _open(sqs_config)

        with pytest.raises(UnavailableError, match="1 of 2"):
            await publisher.publish_batch("events", [b"a", b"b"])
        resend = cast(
            "list[object]",
            cast(
                "list[object]",
                cast("AsyncMock", aws_client.send_message_batch).await_args_list,
            )[1].kwargs["Entries"],
        )
        assert [dig(e, "MessageBody") for e in resend] == ["b"]

        cast("MagicMock", aws_client.send_message_batch).side_effect = [
            failed,
            {"Successful": [{"Id": "1"}], "Failed": []},
        ]
        await publisher.publish_batch("events", [b"a", b"b"])

    @staticmethod
    @pytest.mark.asyncio
    async def test_publish_raises_when_not_initialized(sqs_config: SQSConfig) -> None:
        """Test that publish raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Using the publisher before async initialization would send to a None client
          - Fail-fast with a clear error prevents silent message loss
          - Service startup ordering bugs are caught immediately with this guard

        **What it tests:**
          - RuntimeError with "not initialized" message is raised

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.

        """
        publisher = SQSPublisher(sqs_config)
        with pytest.raises(AppRuntimeError, match="not initialized"):
            await publisher.publish("topic", b"data")

    @staticmethod
    @pytest.mark.asyncio
    async def test_publish_batch_raises_when_not_initialized(
        sqs_config: SQSConfig,
    ) -> None:
        """Test that publish_batch raises RuntimeError before the client is initialized.

        **Why this test is important:**
          - Batch operations share the same initialization guard as single publish
          - Uninitialized batch calls would silently lose multiple messages at once
          - Consistent error behavior across publish and publish_batch simplifies error
            handling

        **What it tests:**
          - RuntimeError with "not initialized" message is raised

        Args:
            sqs_config: An SQS config pointed at the local ElasticMQ defaults.

        """
        publisher = SQSPublisher(sqs_config)
        with pytest.raises(AppRuntimeError, match="not initialized"):
            await publisher.publish_batch("topic", [b"data"])
