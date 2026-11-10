"""Subscribe-time checks of an SQS queue's settings.

They must match a subscriber's ``DeliveryConfig``.

The neutral settings live in config, but SQS enforces them on the queue (its redrive
policy and visibility timeout), so a mismatch would silently change behaviour: a different
``maxReceiveCount`` dead-letters at another count, and a handler that outlives the
visibility timeout is redelivered while it is still running. The check reads the queue's
attributes once per subscribed topic, only when a setting needs it, under
``SQSConfig.attributes_timeout_seconds``.
"""

from __future__ import annotations

import asyncio
import json
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError, UnavailableError

if TYPE_CHECKING:
    from types_aiobotocore_sqs import SQSClient

    from techai_webutils.clients.messaging.config import DeliveryConfig

_DEFAULT_VISIBILITY_SECONDS = 30.0
"""SQS's visibility timeout when a queue reports none."""


async def check_queue(
    client: SQSClient, queue_url: str, delivery: DeliveryConfig, wait_seconds: float
) -> None:
    """Check ``queue_url``'s redrive policy and visibility timeout against ``delivery``.

    Does nothing (and makes no call) unless ``delivery.checks_queue``.

    A callee raises ``AppValueError``: The queue's settings disagree with ``delivery``
    (``INVALID_INPUT``).

    Args:
        client: The SQS client.
        queue_url: The queue.
        delivery: The delivery settings the queue must agree with.
        wait_seconds: The long-poll wait, part of each lease.

    Raises:
        UnavailableError: ``GetQueueAttributes`` did not answer within ``wait_seconds``.

    """
    if not delivery.checks_queue:
        return
    try:
        async with asyncio.timeout(wait_seconds):
            resp = await client.get_queue_attributes(
                QueueUrl=queue_url, AttributeNames=["RedrivePolicy", "VisibilityTimeout"]
            )
    except TimeoutError as exc:
        msg = f"GetQueueAttributes for {queue_url} timed out after {wait_seconds}s"
        raise UnavailableError(msg) from exc
    attributes = resp.get("Attributes", {})
    _check_redrive(queue_url, attributes.get("RedrivePolicy", ""), delivery)
    _check_lease(queue_url, attributes.get("VisibilityTimeout", ""), delivery)


def _check_redrive(queue_url: str, raw_policy: str, delivery: DeliveryConfig) -> None:
    """Check the redrive policy's count and target.

    They must match ``max_deliveries`` and the DLQ topic.

    Args:
        queue_url: The queue.
        raw_policy: The queue's ``RedrivePolicy`` JSON.
        delivery: The delivery settings.

    Raises:
        AppValueError: Queue ``queue_url`` redrives after ``count`` receives, but
            max_deliveries is ``delivery.max_deliveries``; queue ``queue_url``
            dead-letters to ``target``, not ``topic``.

    """
    if delivery.max_deliveries is None:
        return
    policy: dict[str, object] = json.loads(raw_policy) if raw_policy else {}
    count = policy.get("maxReceiveCount")
    if count is None or int(str(count)) != delivery.max_deliveries:
        msg = (
            f"queue {queue_url} redrives after {count} receives, "
            f"but max_deliveries is {delivery.max_deliveries}"
        )
        raise AppValueError(msg)
    target = str(policy.get("deadLetterTargetArn", ""))
    topic = delivery.dead_letter_topic
    if topic is not None and target.rsplit(":", 1)[-1] != topic:
        msg = f"queue {queue_url} dead-letters to {target!r}, not {topic!r}"
        raise AppValueError(msg)


def _check_lease(queue_url: str, raw_visibility: str, delivery: DeliveryConfig) -> None:
    """Check ``max_processing_seconds`` is below the queue's visibility timeout.

    Args:
        queue_url: The queue.
        raw_visibility: The queue's ``VisibilityTimeout``.
        delivery: The delivery settings.

    Raises:
        AppValueError: Max_processing_seconds ``limit`` is not below queue ``queue_url``'s
            ``lease``s visibility timeout.

    """
    limit = delivery.max_processing_seconds
    if limit is None:
        return
    lease = float(raw_visibility) if raw_visibility else _DEFAULT_VISIBILITY_SECONDS
    if limit >= lease:
        msg = (
            f"max_processing_seconds {limit} is not below queue {queue_url}'s "
            f"{lease}s visibility timeout"
        )
        raise AppValueError(msg)
