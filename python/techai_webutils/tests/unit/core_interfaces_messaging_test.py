"""Tests for Message dataclass and MessagePublisher/MessageConsumer ABCs."""

import pytest

from techai_webutils.core.interfaces.messaging import (
    Message,
    MessageConsumer,
    MessagePublisher,
)


class TestMessage:
    """Tests for the message."""

    def test_construction(self) -> None:
        """Test that Message preserves its identity/topic/payload and defaults the rest.

        **Why this test is important:**
          - Message is the envelope every SQS consumer reads; a mangled id breaks ack/dedup,
            a wrong topic misroutes the handler, and a corrupted payload poisons processing
          - The default empty metadata and zero timestamp are the documented baseline a
            consumer relies on when the broker supplies neither

        **What it tests:**
          - id, topic, and payload round-trip exactly, and omitting metadata/timestamp
            yields {} and 0 respectively
        """
        msg = Message(id="m1", topic="events", payload=b"hello")
        assert msg.id == "m1"
        assert msg.topic == "events"
        assert msg.payload == b"hello"
        assert msg.metadata == {}
        assert msg.timestamp == 0

    def test_with_metadata(self) -> None:
        """Test that supplied metadata and timestamp are carried on the Message.

        **Why this test is important:**
          - Broker-supplied metadata (trace headers, attributes) and the timestamp drive
            tracing correlation and ordering/age decisions; dropping them would break
            observability and any time-based handling on the consumer side

        **What it tests:**
          - An explicit metadata mapping and timestamp are stored and returned unchanged
        """
        msg = Message(
            id="m2", topic="t", payload=b"", metadata={"key": "val"}, timestamp=123
        )
        assert msg.metadata == {"key": "val"}
        assert msg.timestamp == 123


class TestMessagePublisher:
    """Tests for the message publisher."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that MessagePublisher cannot be instantiated without publish methods.

        **Why this test is important:**
          - MessagePublisher is the produce-side contract for the outbox/SQS path; an
            instantiable stub would accept publish() calls and silently drop events, so
            downstream services would never learn about documents, notifications, etc.
          - Forces every broker adapter to implement real single and batch publishing

        **What it tests:**
          - Instantiating MessagePublisher directly raises TypeError because publish() and
            publish_batch() are abstract
        """
        with pytest.raises(TypeError):
            MessagePublisher()  # type: ignore[abstract]


class TestMessageConsumer:
    """Tests for the message consumer."""

    def test_cannot_instantiate_abc(self) -> None:
        """Test that MessageConsumer cannot be instantiated without subscribe/close.

        **Why this test is important:**
          - MessageConsumer is the consume-side contract; an instantiable stub would accept
            subscribe() and never deliver messages to the handler, so queued work stalls
            with no error surfaced
          - Forces every consumer adapter to implement real subscription and graceful shutdown

        **What it tests:**
          - Instantiating MessageConsumer directly raises TypeError because subscribe() and
            close() are abstract
        """
        with pytest.raises(TypeError):
            MessageConsumer()  # type: ignore[abstract]
