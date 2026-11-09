"""Tests for the ``BacklogProbe`` capability and the broker-neutral backlog gauges."""

from __future__ import annotations

import asyncio
import re
from dataclasses import replace
from pathlib import Path
from typing import TYPE_CHECKING
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.messaging import BacklogGauges
from techai_webutils.clients.messaging.memory import (
    InMemoryBroker,
    InMemoryPublisher,
    InMemorySubscriber,
)
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.core.errors import UnavailableError
from techai_webutils.core.interfaces.messaging_backlog import Backlog, BacklogProbe

if TYPE_CHECKING:
    from techai_webutils.clients.messaging.config import SQSConfig

_GO_MESSAGING = Path(__file__).resolve().parents[4] / "go" / "core" / "interfaces"


def _metrics() -> tuple[MagicMock, dict[str, MagicMock]]:
    """Return a metrics provider mock and the gauge it hands out per metric name.

    Returns:
        A metrics provider mock and the gauge it hands out per metric name.

    """
    gauges: dict[str, MagicMock] = {}

    def gauge(name: str, _help: str, _labels: list[str] | None = None) -> MagicMock:
        return gauges.setdefault(name, MagicMock())

    metrics = MagicMock()
    metrics.gauge.side_effect = gauge
    return metrics, gauges


@pytest.mark.asyncio
async def test_sqs_backlog_feeds_the_gauges(
    sqs_config: SQSConfig, aws_client: AsyncMock
) -> None:
    """The SQS probe reads the queue depth, and the gauges report it by topic.

    **Why this test is important:**
      - Alerts and KEDA scale on these neutral gauges; a wrong attribute or label would
        scale the workers on nothing.

    **What it tests:**
      - With ``ApproximateNumberOfMessages`` 42, ``report`` returns ``Backlog(42, None)``,
        sets ``messaging_backlog_messages{topic="events"}`` to 42, and leaves the age
        gauge unset (SQS has no age attribute).
    """
    aws_client.get_queue_attributes.return_value = {
        "Attributes": {"ApproximateNumberOfMessages": "42"}
    }
    metrics, gauges = _metrics()
    reporter = BacklogGauges(metrics)

    async with SQSSubscriber(sqs_config) as subscriber:
        backlog = await reporter.report(subscriber, "events")

    assert backlog == Backlog(messages=42, oldest_age_seconds=None)
    aws_client.get_queue_attributes.assert_awaited_once_with(
        QueueUrl=sqs_config.queue_url, AttributeNames=["ApproximateNumberOfMessages"]
    )
    gauges["messaging_backlog_messages"].set.assert_called_once_with(42, topic="events")
    gauges["messaging_oldest_age_seconds"].set.assert_not_called()


@pytest.mark.asyncio
async def test_a_known_age_sets_the_age_gauge() -> None:
    """A probe that knows the oldest message's age reports it.

    **Why this test is important:**
      - A broker with lag timestamps (Kafka) must surface its age; the gauge must not stay
        unset because SQS cannot report one.

    **What it tests:**
      - A probe returning an age of 12.5 seconds sets ``messaging_oldest_age_seconds`` to
        12.5.
    """
    probe = AsyncMock(spec=BacklogProbe)
    probe.backlog.return_value = Backlog(messages=3, oldest_age_seconds=12.5)
    metrics, gauges = _metrics()

    await BacklogGauges(metrics).report(probe, "events")

    gauges["messaging_oldest_age_seconds"].set.assert_called_once_with(
        12.5, topic="events"
    )


@pytest.mark.asyncio
async def test_sqs_backlog_call_is_bounded(
    sqs_config: SQSConfig, aws_client: AsyncMock
) -> None:
    """A hung attributes call fails the probe with a coded error.

    **Why this test is important:**
      - The probe runs on a metrics loop; a hung call without a timeout would freeze the
        gauges at their last value with nothing logged.

    **What it tests:**
      - With ``attributes_timeout_seconds=0.01`` and a call that never answers,
        ``backlog`` raises ``UnavailableError``.
    """

    async def hang(**_kwargs: object) -> dict[str, object]:
        await asyncio.sleep(10)
        return {}

    aws_client.get_queue_attributes.side_effect = hang
    config = replace(sqs_config, attributes_timeout_seconds=0.01)

    async with SQSSubscriber(config) as subscriber:
        with pytest.raises(UnavailableError):
            await subscriber.backlog("events")


@pytest.mark.asyncio
async def test_memory_backlog_is_the_queue_length() -> None:
    """The memory probe reports how many messages are queued on the topic.

    **Why this test is important:**
      - Tests of backlog-driven behaviour (scaling, alerts) run on the memory Kind; it
        must report a real depth, not a constant.

    **What it tests:**
      - After three publishes the backlog is ``Backlog(3, None)``.
    """
    broker = InMemoryBroker()
    publisher = InMemoryPublisher(broker)
    for payload in (b"a", b"b", b"c"):
        await publisher.publish("events", payload)

    backlog = await InMemorySubscriber(broker).backlog("events")

    assert backlog == Backlog(messages=3, oldest_age_seconds=None)


def test_backlog_probe_matches_go() -> None:
    """Go declares the same ``BacklogProbe`` method and ``Backlog`` fields.

    **Why this test is important:**
      - The gauges are the contract alerts and the scaler read across both languages; a
        field renamed on one side would drift unnoticed.

    **What it tests:**
      - Go's ``BacklogProbe`` declares ``Backlog``, and Go's ``Backlog`` struct has
        ``Messages`` and ``OldestAgeSeconds``, the snake-case forms of Python's fields.
    """
    source = "\n".join(
        (_GO_MESSAGING / name).read_text(encoding="utf-8")
        for name in ("messaging.go", "messaging_backlog_probe.go")
    )
    probe = re.search(r"type BacklogProbe interface \{(.*?)\n\}", source, re.DOTALL)
    struct = re.search(r"type Backlog struct \{(.*?)\n\}", source, re.DOTALL)
    assert probe is not None
    assert struct is not None
    assert re.findall(r"^\t([A-Z]\w+)\(", probe.group(1), re.MULTILINE) == ["Backlog"]
    go_fields = re.findall(r"^\t([A-Z]\w+) ", struct.group(1), re.MULTILINE)
    snake = {re.sub(r"(?<!^)([A-Z])", r"_\1", name).lower() for name in go_fields}
    assert snake == set(Backlog.__dataclass_fields__)
