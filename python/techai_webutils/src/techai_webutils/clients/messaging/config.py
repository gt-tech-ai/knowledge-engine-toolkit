"""SQS client configuration."""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.types.aws_client import AwsClientKwargs


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
    """Static access key id; empty falls back to the default AWS credential chain (IRSA)."""
    secret_key: str = ""
    """Static secret access key; empty falls back to the default AWS credential chain."""
    max_messages: int = 10
    """Maximum messages fetched per receive call (SQS caps this at 10)."""
    wait_time_seconds: int = 20
    """Long-poll wait time, in seconds, for each receive call (0 = short poll)."""

    def client_kwargs(self) -> AwsClientKwargs:
        """Keyword args for ``aiobotocore``'s ``create_client("sqs", ...)``.

        Empty ``endpoint``/``access_key``/``secret_key`` collapse to ``None`` so the SDK
        resolves the real AWS regional endpoint and the default credential chain (IRSA in
        cluster) instead of dialing the local ElasticMQ dev defaults (``http://localhost:9324``
        + ``local`` static creds). ``aiobotocore`` rejects an empty-string ``endpoint_url``
        outright (``ValueError: Invalid endpoint:``), so passing the raw empty value crashes a
        cloud consumer on startup. Centralizing here keeps every SQS client (publisher,
        subscriber, DLQ backend, per-app adapters) on one normalization — a new client that
        forgets the ``or None`` guard cannot reintroduce the bug. Mirrors the Go SQS client and
        the S3 client's kind gate.
        """
        return {
            "endpoint_url": self.endpoint or None,
            "region_name": self.region,
            "aws_access_key_id": self.access_key or None,
            "aws_secret_access_key": self.secret_key or None,
        }
