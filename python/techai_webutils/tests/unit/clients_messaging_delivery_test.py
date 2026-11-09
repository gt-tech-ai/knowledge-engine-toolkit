"""Tests for broker-neutral delivery settings.

(``DeliveryConfig``) and their SQS and memory mappings.

Retry backoff, dead-lettering, the processing-time limit, lease extension, the per-topic
queue map, and the publish-side payload limit. Every setting defaults to unset, which must
keep today's behaviour, including making no ``GetQueueAttributes`` call.
"""

from __future__ import annotations

import asyncio
import json
from contextlib import AsyncExitStack
from dataclasses import replace
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock

import pytest

from techai_webutils.clients.messaging import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
)
from techai_webutils.clients.messaging.config import DeliveryConfig
from techai_webutils.clients.messaging.memory import (
    InMemoryBroker,
    InMemoryPublisher,
    InMemorySubscriber,
)
from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.errors import AppRuntimeError, AppValueError, UnavailableError

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message, MessageHandler

_EXPECTED_D = 8.0
_EXPECTED_COUNT = 3

_DLQ_ARN = "arn:aws:sqs:us-east-1:000000000000:events-dlq"


def _raw(n: int, receives: int = 1) -> dict[str, object]:
    """Return received SQS message ``n`` on its ``receives``-th delivery.

    Returns:
        Received SQS message ``n`` on its ``receives``-th delivery.

    """
    return {
        "MessageId": f"m-{n}",
        "Body": "{}",
        "ReceiptHandle": f"r-{n}",
        "Attributes": {"ApproximateReceiveCount": str(receives)},
    }


async def _consume(
    subscriber: SQSSubscriber,
    aws_client: AsyncMock,
    polls: list[list[dict[str, object]]],
    handler: MessageHandler,
    topic: str = "events",
) -> None:
    """Run ``subscribe`` over the given receive batches, closing after the last."""
    batches = iter(polls)

    async def receive(**_kwargs: object) -> dict[str, object]:
        batch = next(batches, None)
        if batch is None:
            await subscriber.close()
            return {"Messages": []}
        return {"Messages": batch}

    aws_client.receive_message.side_effect = receive
    async with subscriber:
        await subscriber.subscribe(topic, handler)


def _queue(
    redrive: dict[str, object] | None, visibility: str = "300"
) -> dict[str, object]:
    """Return a ``GetQueueAttributes`` response with ``redrive`` and ``visibility``.

    Returns:
        A ``GetQueueAttributes`` response with ``redrive`` and ``visibility``.

    """
    attributes = {"VisibilityTimeout": visibility}
    if redrive is not None:
        attributes["RedrivePolicy"] = json.dumps(redrive)
    return {"Attributes": attributes}


async def _failing(_message: Message) -> None:
    """Fail every message, after yielding to the loop as a real handler would.

    Raises:
        AppRuntimeError: Boom.

    """
    await asyncio.sleep(0)
    msg = "boom"
    raise AppRuntimeError(msg)


class TestDeliveryConfig:
    """``DeliveryConfig`` validation and backoff arithmetic."""

    @staticmethod
    def test_backoff_doubles_up_to_its_cap() -> None:
        """Delivery ``k`` waits ``min(cap, base * 2 ** (k - 1))``.

        No backoff gives ``None``.

        **Why this test is important:**
          - Every Kind derives its retry delay from this one function, so a wrong curve
            retries a failing message too fast (hammering a struggling dependency) or too
            slowly.

        **What it tests:**
          - With ``(1, 10)`` deliveries 1..5 wait 1, 2, 4, 8 and 10 seconds; unset is
            ``None``.
        """
        config = DeliveryConfig(retry_backoff=(1.0, 10.0))
        assert [config.backoff_seconds(k) for k in range(1, 6)] == [1, 2, 4, 8, 10]
        assert DeliveryConfig().backoff_seconds(3) is None

    @staticmethod
    def test_full_jitter_draws_up_to_the_delay() -> None:
        """With ``jitter`` delivery ``k`` waits a random time in ``[0, delay]``.

        **Why this test is important:**
          - Messages that failed together (a dependency outage) would otherwise all retry
            together and knock the recovering dependency over again.

        **What it tests:**
          - Over 200 draws of delivery 4 under ``(1, 10)`` every wait is in ``[0, 8]`` and
            they are not all equal.
        """
        config = DeliveryConfig(retry_backoff=(1.0, 10.0), jitter=True)
        draws = [config.backoff_seconds(4) for _ in range(200)]
        assert all(d is not None and 0.0 <= d <= _EXPECTED_D for d in draws)
        assert len(set(draws)) > 1

    @staticmethod
    @pytest.mark.parametrize(
        "build",
        [
            lambda: DeliveryConfig(max_deliveries=0),
            lambda: DeliveryConfig(retry_backoff=(0.0, 1.0)),
            lambda: DeliveryConfig(retry_backoff=(5.0, 1.0)),
            lambda: DeliveryConfig(max_processing_seconds=0.0),
            lambda: DeliveryConfig(dead_letter_topic="dlq"),
        ],
    )
    def test_rejects_settings_no_broker_can_honour(
        build: Callable[[], DeliveryConfig],
    ) -> None:
        """An impossible setting is a coded error at construction.

        **Why this test is important:**
          - A zero delivery count or an inverted backoff would otherwise surface only as
            odd redelivery in production; a dead-letter topic without a delivery count
            never fires.

        **What it tests:**
          - Each listed setting raises ``AppValueError`` (``INVALID_INPUT``).
        """
        with pytest.raises(AppValueError):
            build()


class TestSQSDelivery:
    """The SQS mapping: visibility backoff, subscribe-time checks, and lease extension."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_failed_delivery_backs_off_by_receive_count(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A handler failing on delivery ``k`` hides the message for.

        That delivery's backoff.

        **Why this test is important:**
          - Without a backoff a failing message reappears after the full visibility
            timeout every time, so a transient failure either retries too slowly or
            hammers a dependency.

        **What it tests:**
          - With ``retry_backoff=(2, 60)`` a message on its third receive reaches the
            handler as delivery 3, gets ``ChangeMessageVisibility`` of 8 seconds, is not
            deleted, and the receive asks for ``ApproximateReceiveCount``.
        """
        subscriber = SQSSubscriber(
            sqs_config, delivery=DeliveryConfig(retry_backoff=(2.0, 60.0))
        )
        seen: list[int] = []

        async def failing(message: Message) -> None:
            seen.append(message.delivery)
            await _failing(message)

        await _consume(subscriber, aws_client, [[_raw(0, receives=3)]], failing)

        assert seen == [3]
        aws_client.change_message_visibility.assert_awaited_once_with(
            QueueUrl=sqs_config.queue_url, ReceiptHandle="r-0", VisibilityTimeout=8
        )
        aws_client.delete_message.assert_not_awaited()
        request = aws_client.receive_message.await_args_list[0].kwargs
        assert request["MessageSystemAttributeNames"] == ["ApproximateReceiveCount"]

    @staticmethod
    @pytest.mark.asyncio
    async def test_default_config_makes_no_attributes_call(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A subscriber with no delivery settings starts exactly as before.

        **Why this test is important:**
          - The settings are opt-in; an unconditional ``GetQueueAttributes`` would need a
            new IAM grant in every consumer and cost a call per start.

        **What it tests:**
          - The default subscriber never calls ``get_queue_attributes`` or
            ``change_message_visibility``, and its receive does not ask for system
            attributes.
        """
        await _consume(SQSSubscriber(sqs_config), aws_client, [[_raw(0)]], _failing)

        aws_client.get_queue_attributes.assert_not_awaited()
        aws_client.change_message_visibility.assert_not_awaited()
        request = aws_client.receive_message.await_args_list[0].kwargs
        assert "MessageSystemAttributeNames" not in request

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(
        ("delivery", "redrive", "matches"),
        [
            (DeliveryConfig(max_deliveries=5), {"maxReceiveCount": 5}, True),
            (DeliveryConfig(max_deliveries=5), {"maxReceiveCount": "3"}, False),
            (DeliveryConfig(max_deliveries=5), None, False),
            (
                DeliveryConfig(max_deliveries=5, dead_letter_topic="events-dlq"),
                {"maxReceiveCount": 5, "deadLetterTargetArn": _DLQ_ARN},
                True,
            ),
            (
                DeliveryConfig(max_deliveries=5, dead_letter_topic="other-dlq"),
                {"maxReceiveCount": 5, "deadLetterTargetArn": _DLQ_ARN},
                False,
            ),
        ],
    )
    async def test_max_deliveries_must_match_the_redrive_policy(
        sqs_config: SQSConfig,
        aws_client: AsyncMock,
        delivery: DeliveryConfig,
        redrive: dict[str, object] | None,
        *,
        matches: bool,
    ) -> None:
        """``max_deliveries``.

        And ``dead_letter_topic`` are checked against the queue's redrive.

        **Why this test is important:**
          - SQS, not the client, dead-letters a message; if the queue's count or target
            differs from the config, messages are dead-lettered at another count or to
            another queue.

        **What it tests:**
          - A matching policy subscribes normally; a different count, a missing policy, or
            a different target queue raises ``AppValueError`` before any receive.
        """
        aws_client.get_queue_attributes.return_value = _queue(redrive)
        subscriber = SQSSubscriber(sqs_config, delivery=delivery)

        if matches:
            await _consume(subscriber, aws_client, [], AsyncMock())
            return
        with pytest.raises(AppValueError):
            await _consume(subscriber, aws_client, [], AsyncMock())
        aws_client.receive_message.assert_not_awaited()

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize(("limit", "ok"), [(299.0, True), (300.0, False)])
    async def test_processing_deadline_must_be_below_lease(
        sqs_config: SQSConfig, aws_client: AsyncMock, limit: float, *, ok: bool
    ) -> None:
        """``max_processing_seconds`` must be below the queue's visibility timeout.

        **Why this test is important:**
          - A handler allowed to run as long as the lease is redelivered to another
            consumer while it is still running, so the work is done twice.

        **What it tests:**
          - Against a 300-second visibility timeout, 299 seconds subscribes and 300 raises
            ``AppValueError``.
        """
        aws_client.get_queue_attributes.return_value = _queue(None, visibility="300")
        subscriber = SQSSubscriber(
            sqs_config, delivery=DeliveryConfig(max_processing_seconds=limit)
        )

        if ok:
            await _consume(subscriber, aws_client, [], AsyncMock())
            return
        with pytest.raises(AppValueError, match="visibility timeout"):
            await _consume(subscriber, aws_client, [], AsyncMock())

    @staticmethod
    @pytest.mark.asyncio
    async def test_attributes_call_is_bounded(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A hung ``GetQueueAttributes`` fails the subscribe with a coded error.

        **Why this test is important:**
          - Every external call needs a timeout; a hung check would stall the consumer's
            start forever with nothing logged.

        **What it tests:**
          - With a 10 ms timeout and a call that never answers, ``subscribe`` raises
            ``UnavailableError``.
        """

        async def hang(**_kwargs: object) -> dict[str, object]:
            await asyncio.sleep(10)
            return {}

        aws_client.get_queue_attributes.side_effect = hang
        subscriber = SQSSubscriber(
            replace(sqs_config, attributes_timeout_seconds=0.01),
            delivery=DeliveryConfig(max_deliveries=3),
        )

        with pytest.raises(UnavailableError):
            await _consume(subscriber, aws_client, [], AsyncMock())

    @staticmethod
    @pytest.mark.asyncio
    async def test_lease_extender_extends_visibility(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """A handler's ``extend`` call hides its message for the requested time.

        **Why this test is important:**
          - A long handler (a large parse) must keep its message from reappearing mid-run;
            the heartbeat goes through the neutral ``LeaseExtender``, not an SQS call in
            app code.

        **What it tests:**
          - ``extend(message, 90.2)`` from inside the handler calls
            ``ChangeMessageVisibility`` with the message's receipt handle and 91 seconds;
            after the handler returns, ``extend`` raises ``AppValueError``.
        """
        subscriber = SQSSubscriber(sqs_config)
        seen: list[Message] = []

        async def handler(message: Message) -> None:
            seen.append(message)
            await subscriber.extend(message, 90.2)

        await _consume(subscriber, aws_client, [[_raw(0)]], handler)

        aws_client.change_message_visibility.assert_awaited_once_with(
            QueueUrl=sqs_config.queue_url, ReceiptHandle="r-0", VisibilityTimeout=91
        )
        with pytest.raises(AppValueError):
            await subscriber.extend(seen[0], 10)

    @staticmethod
    @pytest.mark.asyncio
    async def test_topics_use_their_mapped_queues(
        sqs_config: SQSConfig, aws_client: AsyncMock
    ) -> None:
        """With ``topic_queue_urls`` each topic sends to and receives from its own queue.

        **Why this test is important:**
          - Split stages (parse, index) use one queue each; a publisher that sent every
            topic to ``queue_url`` would feed the wrong stage.

        **What it tests:**
          - ``parse`` and ``index`` publish to their two URLs, a subscriber of ``index``
            receives from its URL, and an unmapped topic raises ``AppValueError``.
        """
        config = replace(
            sqs_config,
            topic_queue_urls={"parse": "http://q/parse", "index": "http://q/index"},
        )
        aws_client.send_message_batch.return_value = {"Successful": [], "Failed": []}
        publisher = await AsyncExitStack().enter_async_context(SQSPublisher(config))

        await publisher.publish("parse", b"a")
        await publisher.publish_batch("index", [b"b"])
        with pytest.raises(AppValueError):
            await publisher.publish("other", b"c")
        await _consume(SQSSubscriber(config), aws_client, [], AsyncMock(), topic="index")

        assert aws_client.send_message.await_args.kwargs["QueueUrl"] == "http://q/parse"
        assert (
            aws_client.send_message_batch.await_args.kwargs["QueueUrl"]
            == "http://q/index"
        )
        assert (
            aws_client.receive_message.await_args.kwargs["QueueUrl"] == "http://q/index"
        )


class TestMemoryDelivery:
    """The memory mapping: redelivery with backoff, then the dead-letter topic."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_redelivers_then_dead_letters() -> None:
        """A failing message is delivered ``max_deliveries`` times.

        Then moved to the DLQ topic.

        **Why this test is important:**
          - The memory Kind stands in for SQS in tests of retry handling; if it dropped
            failures, those tests would pass against a broker that never retries.

        **What it tests:**
          - With ``max_deliveries=3`` and a 10 ms backoff the handler sees the message
            three times, as deliveries 1, 2 and 3, then it is on ``events-dlq``; a message
            that succeeds is handled once.
        """
        broker = InMemoryBroker()
        delivery = DeliveryConfig(
            max_deliveries=3, retry_backoff=(0.01, 0.01), dead_letter_topic="events-dlq"
        )
        subscriber = InMemorySubscriber(broker, delivery=delivery)
        attempts: list[str] = []
        deliveries: list[int] = []

        async def handler(message: Message) -> None:
            attempts.append(message.id)
            if message.payload == b"bad":
                deliveries.append(message.delivery)
                await _failing(message)

        publisher = InMemoryPublisher(broker)
        await publisher.publish("events", b"bad")
        await publisher.publish("events", b"good")
        consumer = asyncio.create_task(subscriber.subscribe("events", handler))
        dead = await asyncio.wait_for(broker.queue("events-dlq").get(), timeout=2)
        await subscriber.close()
        await consumer

        assert dead.payload == b"bad"
        assert deliveries == [1, 2, 3]
        assert attempts.count("mem-1") == _EXPECTED_COUNT
        assert attempts.count("mem-2") == 1

    @staticmethod
    @pytest.mark.asyncio
    async def test_without_max_deliveries_a_failure_is_dropped() -> None:
        """The default keeps today's drop-on-failure behaviour.

        **Why this test is important:**
          - Redelivery is opt-in; a consumer that has not configured it must see no
            change.

        **What it tests:**
          - A failing message is handled once and is not requeued.
        """
        broker = InMemoryBroker()
        subscriber = InMemorySubscriber(broker)
        attempts: list[str] = []

        async def handler(message: Message) -> None:
            attempts.append(message.id)
            await subscriber.close()
            await _failing(message)

        await InMemoryPublisher(broker).publish("events", b"bad")
        await subscriber.subscribe("events", handler)

        assert attempts == ["mem-1"]
        assert broker.queue("events").empty()


class TestPayloadLimit:
    """``MessagingConfig.max_payload_bytes`` is enforced before send by both Kinds."""

    @staticmethod
    @pytest.mark.asyncio
    @pytest.mark.parametrize("kind", [MessagingKind.MEMORY, MessagingKind.SQS])
    async def test_oversized_payload_is_rejected_before_send(
        sqs_config: SQSConfig, aws_client: AsyncMock, kind: MessagingKind
    ) -> None:
        """A payload over the limit raises ``INVALID_INPUT`` and nothing is sent.

        **Why this test is important:**
          - SQS rejects a body over 256 KiB with a transport error after the round trip; a
            batch with one oversized entry would send its siblings and fail half-way.

        **What it tests:**
          - With ``max_payload_bytes=8`` a 9-byte ``publish`` and a batch holding one
            9-byte entry raise ``AppValueError``, an 8-byte payload is accepted, and no
            batch call is made.
        """
        aws_client.send_message_batch.return_value = {"Successful": [], "Failed": []}
        config = MessagingConfig(sqs=sqs_config, kind=kind, max_payload_bytes=8)
        publisher = new_messaging_from_config(config)

        async with publisher:
            with pytest.raises(AppValueError):
                await publisher.publish("events", b"x" * 9)
            with pytest.raises(AppValueError):
                await publisher.publish_batch("events", [b"ok", b"x" * 9])
            await publisher.publish("events", b"x" * 8)

        aws_client.send_message_batch.assert_not_awaited()
