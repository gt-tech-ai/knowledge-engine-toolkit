"""Messaging configuration.

Broker-neutral delivery settings and the SQS client's settings.

``DeliveryConfig`` names retry, dead-letter and processing-time limits without broker
terms; each Kind maps them onto its broker (SQS: ``ChangeMessageVisibility`` and the
queue's redrive policy). Every field defaults to ``None``, today's behaviour: nothing is
retried by the client, and no startup check (or its ``GetQueueAttributes`` call) runs.
"""

from __future__ import annotations

import random
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from techai_webutils.core.types.aws_client import AwsClientKwargs

_RNG = random.SystemRandom()
"""Draws the full-jitter delays (the OS source keeps the weak-PRNG scanners clean)."""

DEFAULT_MAX_PAYLOAD_BYTES = 262_144
"""The largest message body a publisher accepts: 256 KiB, the strictest supported broker
(SQS)."""


@dataclass(frozen=True)
class DeliveryConfig:
    """Broker-neutral delivery settings for a subscriber.

    ``None`` leaves a setting unchecked.
    """

    max_deliveries: int | None = None
    """Deliveries before a failing message goes to ``dead_letter_topic``. SQS checks it
    equals the queue's redrive ``maxReceiveCount`` at subscribe time; memory redelivers up
    to it."""
    retry_backoff: tuple[float, float] | None = None
    """``(base, cap)`` seconds: delivery ``k`` of a failing message is retried after
    ``min(cap, base * 2 ** (k - 1))``. SQS sets it as the message's visibility timeout."""
    dead_letter_topic: str | None = None
    """Where a message lands after ``max_deliveries`` failures (SQS: the redrive target
    queue's name, checked at subscribe time)."""
    jitter: bool = False
    """Full jitter on ``retry_backoff``: delivery ``k`` waits a uniform random time up to
    its delay, so failures that happened together are not all retried together."""
    max_processing_seconds: float | None = None
    """The longest a handler may run. It must be below the message lease (SQS: the queue's
    visibility timeout), or the message would be redelivered while still being handled."""

    def __post_init__(self) -> None:
        """Reject settings no broker can honour.

        Raises:
            AppValueError: A count or duration is not positive, the backoff cap is below
                its base, or a dead-letter topic is set without ``max_deliveries``.

        """
        if self.max_deliveries is not None and self.max_deliveries < 1:
            msg = "max_deliveries must be at least 1"
            raise AppValueError(msg)
        if self.retry_backoff is not None:
            base, cap = self.retry_backoff
            if base <= 0 or cap < base:
                msg = "retry_backoff must be (base, cap) with 0 < base <= cap"
                raise AppValueError(msg)
        if self.max_processing_seconds is not None and self.max_processing_seconds <= 0:
            msg = "max_processing_seconds must be positive"
            raise AppValueError(msg)
        if self.dead_letter_topic is not None and self.max_deliveries is None:
            msg = "dead_letter_topic needs max_deliveries"
            raise AppValueError(msg)

    @property
    def checks_queue(self) -> bool:
        """Whether a setting needs the broker's queue settings checked.

        At subscribe time.
        """
        return (
            self.max_deliveries is not None
            or self.dead_letter_topic is not None
            or self.max_processing_seconds is not None
        )

    def backoff_seconds(self, delivery: int) -> float | None:
        """Return the retry delay after failed delivery ``delivery`` (1-based).

        Or ``None``.

        With ``jitter`` the delay is drawn uniformly from ``[0, delay]`` (full jitter).
        """
        if self.retry_backoff is None:
            return None
        base, cap = self.retry_backoff
        delay = min(cap, base * 2 ** max(delivery - 1, 0))
        return _RNG.uniform(0.0, delay) if self.jitter else delay


@dataclass(frozen=True)
class SQSConfig:
    """Configuration for SQS client connections."""

    endpoint: str
    """SQS-compatible endpoint URL; empty resolves the real AWS regional endpoint."""
    region: str
    """AWS region for the SQS client (e.g. ``us-east-1``)."""
    queue_url: str
    """Full URL of the SQS queue to send to or receive from."""
    access_key: str = ""
    """Static access key id; empty falls back to the default AWS credential chain
    (IRSA)."""
    secret_key: str = ""
    """Static secret access key; empty falls back to the default AWS credential chain."""
    max_messages: int = 10
    """Maximum messages fetched per receive call (SQS caps this at 10)."""
    wait_time_seconds: int = 20
    """Long-poll wait time, in seconds, for each receive call (0 = short poll)."""
    settle_batch: bool = False
    """Settle (delete) a receive batch's handled messages with one ``DeleteMessageBatch``
    call instead of one ``DeleteMessage`` each. ``False`` keeps the per-message delete."""
    publish_max_in_flight: int = 8
    """Most ``SendMessageBatch`` calls ``publish_batch`` keeps in flight at once."""
    topic_queue_urls: Mapping[str, str] = field(default_factory=dict[str, str])
    """Topic → queue URL. Empty (the default) sends and receives every topic on
    ``queue_url``; once set, a topic it does not map is a coded error."""
    attributes_timeout_seconds: float = 5.0
    """Timeout of the subscribe-time ``GetQueueAttributes`` call (run only when a
    ``DeliveryConfig`` setting needs it)."""

    def queue_url_for(self, topic: str) -> str:
        """Return the queue URL ``topic`` is sent to and received from.

        Raises:
            AppValueError: ``topic_queue_urls`` is set and does not map ``topic``.

        """
        if not self.topic_queue_urls:
            return self.queue_url
        url = self.topic_queue_urls.get(topic)
        if url is None:
            msg = f"no queue URL is configured for topic {topic!r}"
            raise AppValueError(msg)
        return url

    def client_kwargs(self) -> AwsClientKwargs:
        """Keyword args for ``aiobotocore``'s ``create_client("sqs", ...)``.

        Empty ``endpoint``/``access_key``/``secret_key`` collapse to ``None`` so the SDK
        resolves the real AWS regional endpoint and the default credential chain (IRSA in
        cluster) instead of dialing the local ElasticMQ dev defaults
        (``http://localhost:9324``
        + ``local`` static creds). ``aiobotocore`` rejects an empty-string
        ``endpoint_url`` outright (``ValueError: Invalid endpoint:``), so passing the raw
        empty value crashes a cloud consumer on startup. Centralizing here keeps every SQS
        client (publisher, subscriber, DLQ backend, per-app adapters) on one normalization
        — a new client that forgets the ``or None`` guard cannot reintroduce the bug.
        Mirrors the Go SQS client and the S3 client's kind gate.
        """
        return {
            "endpoint_url": self.endpoint or None,
            "region_name": self.region,
            "aws_access_key_id": self.access_key or None,
            "aws_secret_access_key": self.secret_key or None,
        }
