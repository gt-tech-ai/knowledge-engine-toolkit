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

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.messaging.builder import (
    MessagingConfig,
    MessagingKind,
    new_messaging_from_config,
    new_messaging_subscriber_from_config,
)
from techai_webutils.clients.messaging.config import SQSConfig
from techai_webutils.clients.messaging.sqs.sqs_subscriber import SQSSubscriber
from techai_webutils.clients.rpc.builder import RpcConfig, new_rpc_channel_from_config
from techai_webutils.clients.storage.builder import (
    StorageConfig,
    StorageKind,
    new_storage_from_config,
)
from techai_webutils.clients.storage.config import S3Config
from techai_webutils.clients.storage.s3.s3_client import S3StorageClient
from techai_webutils.core.errors import AppValueError
from tests.json_path import dig

_S3 = S3Config(endpoint="", bucket="b", region="r")
_SQS = SQSConfig(endpoint="", region="r", queue_url="")


def test_storage_factory_selects_s3_backend() -> None:
    """kind=S3 builds the S3-compatible storage client."""
    client = new_storage_from_config(StorageConfig(s3=_S3, kind=StorageKind.S3))
    assert isinstance(client, S3StorageClient)


def test_storage_factory_rejects_unknown_kind() -> None:
    """An unknown storage kind fails loudly with ValueError."""
    with pytest.raises(AppValueError, match="unknown storage kind"):
        _ = new_storage_from_config(
            StorageConfig(s3=_S3, kind=cast("StorageKind", cast("object", "bogus")))
        )


@pytest.mark.asyncio
async def test_messaging_factory_selects_sqs_publisher_and_subscriber(
    sqs_config: SQSConfig, aws_client: AsyncMock
) -> None:
    """kind=SQS builds the SQS publisher and subscriber.

    The publisher sends to the configured queue.

    Why this test is important:
      - The kind is the one config value that picks the broker; selecting SQS must send to
        SQS, through the client tier, at the configured queue.

    What it tests:
      - A publish through the factory's publisher reaches SQS ``send_message`` on the
        config's queue URL with the topic attribute; the subscriber factory builds an
        ``SQSSubscriber``.

    Args:
        sqs_config: An SQS config pointed at the local ElasticMQ defaults.
        aws_client: The client the patched aiobotocore session opens.

    """
    aws_client.send_message = AsyncMock()
    cfg = MessagingConfig(sqs=sqs_config, kind=MessagingKind.SQS)

    async with new_messaging_from_config(cfg) as publisher:
        await publisher.publish("orders", b"{}")

    sent = cast(
        "dict[str, object]",
        cast("MagicMock", cast("MagicMock", aws_client.send_message).call_args).kwargs,
    )
    assert sent["QueueUrl"] == sqs_config.queue_url
    assert dig(sent, "MessageAttributes", "topic", "StringValue") == "orders"
    assert isinstance(new_messaging_subscriber_from_config(cfg), SQSSubscriber)


def test_messaging_factory_rejects_unknown_kind() -> None:
    """An unknown messaging kind fails loudly for both publisher and subscriber."""
    cfg = MessagingConfig(sqs=_SQS, kind=cast("MessagingKind", cast("object", "bogus")))
    with pytest.raises(AppValueError, match="unknown messaging kind"):
        _ = new_messaging_from_config(cfg)
    with pytest.raises(AppValueError, match="unknown messaging kind"):
        _ = new_messaging_subscriber_from_config(cfg)


@pytest.mark.asyncio
async def test_rpc_factory_builds_grpc_channel() -> None:
    """kind=grpc builds a grpc.aio channel (lazy — no connection opened)."""
    channel = new_rpc_channel_from_config(RpcConfig())
    try:
        assert channel is not None
    finally:
        await channel.close()
