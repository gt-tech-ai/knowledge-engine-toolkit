"""Tests for keyed publishing (``KeyedPublisher``; Go ``interfaces.KeyedPublisher``).

The memory backend carries the key in metadata and keeps publish order; SQS carries it as
the ``partition_key`` attribute and does not order by it; decoration keeps the capability.
"""

from __future__ import annotations

import re
from contextlib import AsyncExitStack
from pathlib import Path
from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.messaging import decorate
from techai_webutils.clients.messaging.memory import InMemoryBroker, InMemoryPublisher
from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.core.interfaces.messaging import (
    PARTITION_KEY_ATTRIBUTE,
    KeyedPublisher,
    MessagePublisher,
)
from techai_webutils.foundation.config import decoration
from techai_webutils.foundation.decorate import Collaborators, Decorations
from tests.json_path import dig

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig
    from techai_webutils.core.interfaces.messaging import Message

_GO_MESSAGING = (
    Path(__file__).resolve().parents[4] / "go" / "core" / "interfaces" / "messaging.go"
)


async def _drain(broker: InMemoryBroker, topic: str, n: int) -> list[Message]:
    """Return the first ``n`` messages queued on ``topic``.

    Returns:
        The first ``n`` messages queued on ``topic``.

    """
    queue = broker.queue(topic)
    return [await queue.get() for _ in range(n)]


@pytest.mark.asyncio
async def test_memory_keyed_keeps_order_and_key() -> None:
    """The memory publisher carries each key in metadata and keeps publish order.

    **Why this test is important:**
      - The memory backend stands in for a partitioned broker in tests; without the key
        and the order, tests of ordered consumers would pass on a broker that never
        orders.

    **What it tests:**
      - ``publish_batch_keyed`` then ``publish_keyed`` queue messages in publish order,
        each with its key under ``partition_key``, and ``ordered_per_key`` is ``True``.
    """
    broker = InMemoryBroker()
    publisher = InMemoryPublisher(broker)

    await publisher.publish_batch_keyed(
        "t", [("doc-1", b"a"), ("doc-2", b"b"), ("doc-1", b"c")]
    )
    await publisher.publish_keyed("t", "doc-1", b"d")

    messages = await _drain(broker, "t", 4)
    assert [m.payload for m in messages] == [b"a", b"b", b"c", b"d"]
    assert [m.metadata[PARTITION_KEY_ATTRIBUTE] for m in messages] == [
        "doc-1",
        "doc-2",
        "doc-1",
        "doc-1",
    ]
    assert publisher.ordered_per_key


@pytest.mark.asyncio
async def test_sqs_keyed_sends_the_partition_key_attribute(
    sqs_config: SQSConfig, aws_client: AsyncMock
) -> None:
    """The SQS publisher sends each key as the ``partition_key`` string attribute.

    **Why this test is important:**
      - A standard queue cannot order by key, but a consumer (or a later move to a
        partitioned broker) still needs it; dropping the key would lose the grouping.

    **What it tests:**
      - ``publish_keyed`` sends the key attribute beside ``topic``;
        ``publish_batch_keyed`` sends 12 items in batches of 10 and 2, each entry with its
        own key; ``ordered_per_key`` is ``False``.
    """
    cast("MagicMock", aws_client.send_message_batch).return_value = {
        "Successful": [],
        "Failed": [],
    }
    publisher = await AsyncExitStack().enter_async_context(SQSPublisher(sqs_config))

    await publisher.publish_keyed("events", "doc-9", b"x")
    keys = ["even", "odd"] * 6
    await publisher.publish_batch_keyed("events", [(k, b"p") for k in keys])

    attrs = cast(
        "dict[str, object]",
        cast("MagicMock", cast("AsyncMock", aws_client.send_message).await_args).kwargs[
            "MessageAttributes"
        ],
    )
    assert attrs[PARTITION_KEY_ATTRIBUTE] == {
        "DataType": "String",
        "StringValue": "doc-9",
    }
    assert dig(attrs, "topic", "StringValue") == "events"
    calls = cast(
        "object", cast("AsyncMock", aws_client.send_message_batch).await_args_list
    )
    assert [
        len(cast("list[object]", c.kwargs["Entries"]))
        for c in cast("list[MagicMock]", calls)
    ] == [10, 2]
    sent = [
        dig(e, "MessageAttributes", PARTITION_KEY_ATTRIBUTE, "StringValue")
        for c in cast("list[MagicMock]", calls)
        for e in cast("list[dict[str, object]]", c.kwargs["Entries"])
    ]
    assert sent == keys
    assert not publisher.ordered_per_key


@pytest.mark.asyncio
async def test_decorate_keeps_the_keyed_capability() -> None:
    """Decorating a keyed publisher yields a keyed publisher that forwards keys.

    **Why this test is important:**
      - Every publisher is decorated at the composition root; a wrapper that dropped the
        keyed methods would silently turn keyed publishing off in production.

    **What it tests:**
      - ``decorate`` over the memory publisher returns a ``KeyedPublisher`` whose keyed
        publishes reach the broker with their keys and whose ``ordered_per_key`` is the
        base's; over an unkeyed publisher it returns one that is not a ``KeyedPublisher``.
    """
    decorations = Decorations(decoration.default_config(), Collaborators())
    broker = InMemoryBroker()
    publisher = decorate(InMemoryPublisher(broker), decorations, "events")
    assert isinstance(publisher, KeyedPublisher)

    await publisher.publish_keyed("t", "k1", b"a")
    await publisher.publish_batch_keyed("t", [("k2", b"b")])

    messages = await _drain(broker, "t", 2)
    assert [m.metadata[PARTITION_KEY_ATTRIBUTE] for m in messages] == ["k1", "k2"]
    assert publisher.ordered_per_key

    plain = decorate(AsyncMock(spec=MessagePublisher), decorations, "events")
    assert not isinstance(plain, KeyedPublisher)


def test_keyed_publisher_matches_go() -> None:
    """The Python ``KeyedPublisher`` methods and attribute name match Go's.

    **Why this test is important:**
      - Go and Python services publish to the same queues; a renamed method or a different
        attribute name on one side would drop keys across the language boundary.

    **What it tests:**
      - Go's ``KeyedPublisher`` declares ``PublishKeyed``, ``PublishBatchKeyed`` and
        ``OrderedPerKey``, whose snake-case forms are Python's abstract members, and Go's
        ``PartitionKeyAttribute`` equals ``PARTITION_KEY_ATTRIBUTE``.
    """
    source = _GO_MESSAGING.read_text(encoding="utf-8")
    body = re.search(r"type KeyedPublisher interface \{(.*?)\n\}", source, re.DOTALL)
    assert body is not None
    go_methods: set[str] = set(
        cast("list[str]", re.findall(r"^\t([A-Z]\w+)\(", body.group(1), re.MULTILINE))
    )
    snake = {re.sub(r"(?<!^)([A-Z])", r"_\1", name).lower() for name in go_methods}
    assert snake == set(KeyedPublisher.__abstractmethods__) - set(
        MessagePublisher.__abstractmethods__
    )
    go_attr = re.search(r'PartitionKeyAttribute = "(\w+)"', source)
    assert go_attr is not None
    assert go_attr.group(1) == PARTITION_KEY_ATTRIBUTE
