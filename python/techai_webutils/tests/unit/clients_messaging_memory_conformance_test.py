"""The transport conformance suite (``tests/contract/messaging_conformance.py``) on the memory Kind."""

from __future__ import annotations

from typing import TYPE_CHECKING

import pytest
from contract.messaging_conformance import CASES, Transport

from techai_webutils.clients.messaging.config import DeliveryConfig
from techai_webutils.clients.messaging.memory import (
    InMemoryBroker,
    InMemoryPublisher,
    InMemorySubscriber,
)

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable


def _transport() -> Transport:
    """Return the memory Kind on a fresh broker, retrying twice with a 10 ms backoff."""
    broker = InMemoryBroker()
    return Transport(
        publisher=lambda limit: InMemoryPublisher(broker, max_payload_bytes=limit),
        subscriber=lambda delivery: InMemorySubscriber(broker, delivery=delivery),
        topic="events",
        dead_letter_topic="events-dlq",
        delivery=DeliveryConfig(
            max_deliveries=2, retry_backoff=(0.01, 0.01), dead_letter_topic="events-dlq"
        ),
        concurrent=False,
    )


@pytest.mark.asyncio
@pytest.mark.parametrize("case", CASES, ids=lambda case: case.__name__)
async def test_memory_kind_conforms(case: Callable[[Transport], Awaitable[None]]) -> None:
    """The memory Kind passes every transport conformance case.

    **Why this test is important:**
      - Tests across the platform use the memory Kind in place of SQS; where it diverges from
        the contract (no redelivery, no keys, no payload limit), those tests pass on behaviour
        production does not have.

    **What it tests:**
      - Each case in ``CASES``: at-least-once delivery, redelivery then dead-lettering, a
        duplicate after a failed attempt, keys and per-key order, serial settling, metadata
        round-trip, and the payload limit.
    """
    await case(_transport())
