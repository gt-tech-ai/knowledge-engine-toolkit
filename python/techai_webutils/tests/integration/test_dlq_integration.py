"""Integration test: SqsDeadLetterBackend against real ElasticMQ.

Unit tests use the stub backend; this proves a dead letter actually lands on a real
SQS-compatible queue with its payload + reason attribute intact — the durable poison
sink a consumer relies on for permanent failures.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING

import pytest

from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.interfaces.dlq import DeadLetter
from techai_webutils.foundation.resilience.dlq import DeadLetterQueue
from techai_webutils.foundation.resilience.sqs_dlq import SqsDeadLetterBackend

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message


@pytest.mark.integration
@pytest.mark.asyncio
async def test_sqs_dlq_backend_sends_letter(sqs_config: SQSConfig) -> None:
    """Test that a DeadLetter is delivered to a real SQS queue with payload + reason.

    **Why this test is important:**
      - The SQS DLQ backend is the production poison sink; a serialization or attribute bug
        (only visible against a real broker) would lose the forensic record of failed documents.

    **What it tests:**
      - send returns True, and draining the queue yields one message with the original payload
        and the ``reason`` attribute propagated.
    """
    letter = DeadLetter(
        id="doc-1",
        payload=b'{"document_id":"doc-1","bad":true}',
        reason="unsupported_format",
        metadata={"document_id": "doc-1"},
    )
    async with SqsDeadLetterBackend(sqs_config) as backend:
        dlq = DeadLetterQueue(backend)
        assert await dlq.send(letter) is True

    received: list[Message] = []
    async with SQSSubscriber(sqs_config) as subscriber:

        async def handler(message: Message) -> None:
            received.append(message)
            await subscriber.close()

        await asyncio.wait_for(subscriber.subscribe("dlq", handler), timeout=20)

    assert len(received) == 1
    assert received[0].payload == letter.payload
    assert received[0].metadata.get("reason") == "unsupported_format"
