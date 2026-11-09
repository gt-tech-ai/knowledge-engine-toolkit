"""The transport conformance suite.

(``tests/contract/messaging_conformance.py``) on real SQS.

Runs every case against ElasticMQ: a source queue whose redrive policy dead-letters after
    two
receives into its own dead-letter queue, mapped to topics through ``topic_queue_urls``.
"""

from __future__ import annotations

import json
import uuid
from dataclasses import replace
from typing import TYPE_CHECKING

import aiobotocore.session
import pytest

from techai_webutils.clients.messaging.config import DeliveryConfig
from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from tests.contract.messaging_conformance import CASES, Transport

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Mapping

    from techai_webutils.clients.messaging.config import SQSConfig

_MAX_DELIVERIES = 2


async def _arn(config: SQSConfig, queue_url: str) -> str:
    """Return the ARN of the queue at ``queue_url``.

    Returns:
        The ARN of the queue at ``queue_url``.

    """
    session = aiobotocore.session.get_session()
    async with session.create_client("sqs", **config.client_kwargs()) as client:
        resp = await client.get_queue_attributes(
            QueueUrl=queue_url, AttributeNames=["QueueArn"]
        )
    return resp.get("Attributes", {})["QueueArn"]


async def _transport(
    sqs_config: SQSConfig,
    make_sqs_queue: Callable[[str, Mapping[str, str] | None], Awaitable[str]],
) -> Transport:
    """Return the SQS Kind on a fresh source queue and dead-letter queue.

    Returns:
        The SQS Kind on a fresh source queue and dead-letter queue.

    """
    suffix = uuid.uuid4().hex[:10]
    topic, dlq_topic = f"conf-{suffix}", f"conf-{suffix}-dlq"
    dlq_url = await make_sqs_queue(dlq_topic, None)
    redrive = {
        "deadLetterTargetArn": await _arn(sqs_config, dlq_url),
        "maxReceiveCount": str(_MAX_DELIVERIES),
    }
    url = await make_sqs_queue(topic, {"RedrivePolicy": json.dumps(redrive)})
    config = replace(sqs_config, topic_queue_urls={topic: url, dlq_topic: dlq_url})
    return Transport(
        publisher=lambda limit: SQSPublisher(config, max_payload_bytes=limit),
        subscriber=lambda delivery: SQSSubscriber(config, delivery=delivery),
        topic=topic,
        dead_letter_topic=dlq_topic,
        delivery=DeliveryConfig(
            max_deliveries=_MAX_DELIVERIES,
            retry_backoff=(1.0, 1.0),
            dead_letter_topic=dlq_topic,
        ),
        concurrent=True,
    )


@pytest.mark.integration
@pytest.mark.asyncio
@pytest.mark.parametrize("case", CASES, ids=lambda case: case.__name__)
async def test_sqs_kind_conforms(
    sqs_config: SQSConfig,
    make_sqs_queue: Callable[[str, Mapping[str, str] | None], Awaitable[str]],
    case: Callable[[Transport], Awaitable[None]],
) -> None:
    """The SQS Kind passes every transport conformance case against a real broker.

    **Why this test is important:**
      - Redrive, visibility backoff, attribute round-trips and batch semantics are SQS
        behaviour that mocks only assume; a real broker proves the mapping of the neutral
        settings.

    **What it tests:**
      - Each case in ``CASES`` against ElasticMQ, including the subscribe-time check that
        the queue's redrive policy matches ``max_deliveries`` and the dead-letter topic.
    """
    await case(await _transport(sqs_config, make_sqs_queue))
