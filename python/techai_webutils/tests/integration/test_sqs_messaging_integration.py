"""Integration tests for the SQS publisher/subscriber against real ElasticMQ.

Unit tests mock the aiobotocore client and assert call shapes; this suite proves
the publish -> long-poll receive -> ack(delete) round-trip works over a real
SQS-compatible broker, including message-attribute (topic) propagation.
"""

from __future__ import annotations

import asyncio
from dataclasses import replace
from typing import TYPE_CHECKING

import aiobotocore.session
import pytest

from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message

_EXPECTED_RECEIVED_COUNT = 25


async def _drain(
    config: SQSConfig, topic: str, want: int, within_s: float = 20.0
) -> list[Message]:
    """Subscribe and collect ``want`` messages, then stop the consumer loop.

    Returns:
        The received messages.

    """
    received: list[Message] = []
    async with SQSSubscriber(config) as subscriber:

        async def handler(message: Message) -> None:
            received.append(message)
            if len(received) >= want:
                await subscriber.close()

        await asyncio.wait_for(subscriber.subscribe(topic, handler), timeout=within_s)
    return received


@pytest.mark.integration
@pytest.mark.asyncio
async def test_publish_then_receive_roundtrip(sqs_config: SQSConfig) -> None:
    """Test that a published message is received with its payload and topic intact.

    **Why this test is important:**
      - Publish->consume is the backbone of any event-driven pipeline built on these
        clients; a serialization or attribute-mapping bug here breaks every async
        workflow and cannot be caught by mocking the broker away

    **What it tests:**
      - The consumer receives exactly the published payload bytes
      - The topic carried via SQS message attributes is propagated to Message.topic
        and Message.metadata
    """
    payload = b'{"type":"doc.uploaded","id":"42"}'
    async with SQSPublisher(sqs_config) as publisher:
        await publisher.publish("events", payload)

    received = await _drain(sqs_config, "events", want=1)

    assert len(received) == 1
    assert received[0].payload == payload
    assert received[0].topic == "events"
    assert received[0].metadata.get("topic") == "events"


@pytest.mark.integration
@pytest.mark.asyncio
async def test_publish_batch_delivers_all_messages(sqs_config: SQSConfig) -> None:
    """Test that publish_batch delivers every message to the queue.

    **Why this test is important:**
      - Batch publishing is used for fan-out (e.g. per-document events); a dropped
        or merged entry means silently lost work that no unit test of the mock would
        reveal

    **What it tests:**
      - All three batched payloads are received (order-independent)
    """
    payloads = [b'{"n":1}', b'{"n":2}', b'{"n":3}']
    async with SQSPublisher(sqs_config) as publisher:
        await publisher.publish_batch("events", payloads)

    received = await _drain(sqs_config, "events", want=len(payloads))

    assert {m.payload for m in received} == set(payloads)


@pytest.mark.integration
@pytest.mark.asyncio
async def test_acked_message_is_not_redelivered(sqs_config: SQSConfig) -> None:
    """Test that a successfully handled message is deleted (not redelivered).

    **Why this test is important:**
      - The subscriber acks by deleting on handler success; if the delete is broken
        the message reappears after its visibility timeout, causing duplicate
        processing — a correctness bug only a real broker exposes

    **What it tests:**
      - After draining one message, a second short subscribe receives nothing
    """
    async with SQSPublisher(sqs_config) as publisher:
        await publisher.publish("events", b'{"once":true}')

    first = await _drain(sqs_config, "events", want=1)
    assert len(first) == 1

    # Nothing left to receive: the second drain times out with an empty result.
    second: list[Message] = []
    async with SQSSubscriber(sqs_config) as subscriber:

        async def handler(message: Message) -> None:
            second.append(message)
            await subscriber.close()

        with pytest.raises(asyncio.TimeoutError):
            await asyncio.wait_for(subscriber.subscribe("events", handler), timeout=4)

    assert second == []


async def _queue_depth(config: SQSConfig) -> tuple[int, int]:
    """Return the queue's (visible, in-flight) message counts.

    Returns:
        The queue's (visible, in-flight) message counts.

    """
    session = aiobotocore.session.get_session()
    async with session.create_client("sqs", **config.client_kwargs()) as client:
        attrs = await client.get_queue_attributes(
            QueueUrl=config.queue_url,
            AttributeNames=[
                "ApproximateNumberOfMessages",
                "ApproximateNumberOfMessagesNotVisible",
            ],
        )
    values = attrs.get("Attributes", {})
    return (
        int(values.get("ApproximateNumberOfMessages", "0")),
        int(values.get("ApproximateNumberOfMessagesNotVisible", "0")),
    )


@pytest.mark.integration
@pytest.mark.asyncio
async def test_batch_settle_deletes_every_received_message(sqs_config: SQSConfig) -> None:
    """Test that settling with DeleteMessageBatch removes every handled message.

    From the queue.

    **Why this test is important:**
      - A batch delete that silently missed entries would leave messages in flight, to be
        redelivered and handled again after their visibility timeout; only a real broker
        shows whether the receipt handles in a batch call are honoured

    **What it tests:**
      - 25 messages published with ``publish_batch`` (three batch calls under the
        in-flight bound) are all received through a ``settle_batch`` subscriber, after
        which the queue has no visible and no in-flight messages
    """
    config = replace(sqs_config, settle_batch=True, publish_max_in_flight=2)
    async with SQSPublisher(config) as publisher:
        await publisher.publish_batch(
            "events", [f'{{"n":{n}}}'.encode() for n in range(25)]
        )

    received = await _drain(config, "events", want=25)

    assert len(received) == _EXPECTED_RECEIVED_COUNT
    assert await _queue_depth(config) == (0, 0)
