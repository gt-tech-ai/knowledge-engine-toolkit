"""Tests for the client tier-root ``*_from_config`` factories' backend selection.

Why these tests are important:
  - Each factory selects a backend by ``kind`` (mirroring the Go ``NewFromConfig``
    contract) and MUST fail loudly on an unknown kind rather than return a broken
    or ``None`` client. The all-stubs test covers the in-memory paths; these cover
    the real-backend selection (constructed offline — the SDK clients are lazy) and
    the fail-loud unknown-kind branch.

What they test:
  - The real (S3 / SQS / gRPC) kind returns the concrete backend type; an unknown
    kind raises ValueError.
"""

from __future__ import annotations

import pytest

from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.sqs.sqs_publisher import SQSPublisher
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.clients.rpc.builder import RpcConfig, new_rpc_channel_from_config
from techai_webutils.clients.storage.builder import (
    StorageConfig,
    StorageKind,
    new_storage_from_config,
)
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.clients.storage.s3.s3_client import S3StorageClient

_S3 = S3Config(endpoint="", bucket="b", region="r")
_SQS = SQSConfig(endpoint="", region="r", queue_url="")


def test_storage_factory_selects_s3_backend() -> None:
    """kind=S3 builds the S3-compatible storage client."""
    client = new_storage_from_config(StorageConfig(s3=_S3, kind=StorageKind.S3))
    assert isinstance(client, S3StorageClient)


def test_storage_factory_rejects_unknown_kind() -> None:
    """An unknown storage kind fails loudly with ValueError."""
    with pytest.raises(ValueError, match="unknown storage kind"):
        new_storage_from_config(StorageConfig(s3=_S3, kind="bogus"))  # type: ignore[arg-type]


def test_messaging_factory_selects_sqs_publisher_and_subscriber() -> None:
    """kind=SQS builds the SQS publisher and subscriber backends."""
    cfg = MessagingConfig(sqs=_SQS, kind=MessagingKind.SQS)
    assert isinstance(new_messaging_from_config(cfg), SQSPublisher)
    assert isinstance(new_messaging_subscriber_from_config(cfg), SQSSubscriber)


def test_messaging_factory_rejects_unknown_kind() -> None:
    """An unknown messaging kind fails loudly for both publisher and subscriber."""
    cfg = MessagingConfig(sqs=_SQS, kind="bogus")  # type: ignore[arg-type]
    with pytest.raises(ValueError, match="unknown messaging kind"):
        new_messaging_from_config(cfg)
    with pytest.raises(ValueError, match="unknown messaging kind"):
        new_messaging_subscriber_from_config(cfg)


@pytest.mark.asyncio
async def test_rpc_factory_builds_grpc_channel() -> None:
    """kind=grpc builds a grpc.aio channel (lazy — no connection opened)."""
    channel = new_rpc_channel_from_config(RpcConfig())
    try:
        assert channel is not None
    finally:
        await channel.close()
