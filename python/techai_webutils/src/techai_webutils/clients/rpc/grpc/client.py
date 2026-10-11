"""Async gRPC client channel factory.

Creates configured ``grpc.aio`` channels with a 16 MB max message size and a bounded
max-concurrent-streams. (Channel pooling / idle timeout are not configured here — a single
``grpc.aio`` channel already multiplexes concurrent RPCs.)
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import grpc

_16MB = 16 * 1024 * 1024
"""Default gRPC max send/receive message size, in bytes (16 MiB)."""


@dataclass
class ChannelConfig:
    """gRPC channel configuration (the options actually applied by ``create_channel``)."""

    target: str = "localhost:50051"
    """Default dial target when ``create_channel`` is called without an explicit target."""
    max_send_message_length: int = _16MB
    """Max outbound message size in bytes."""
    max_receive_message_length: int = _16MB
    """Max inbound message size in bytes."""
    max_concurrent_streams: int = 100
    """Cap on concurrent HTTP/2 streams multiplexed over the channel."""


def create_channel(
    target: str | None = None,
    config: ChannelConfig | None = None,
) -> grpc.aio.Channel:
    """Create a configured async ``grpc.aio`` channel.

    ``target`` overrides ``config.target`` when provided; ``config`` defaults to ``ChannelConfig()``.
    Returns an insecure channel carrying the message-size + concurrent-stream options.
    """
    cfg = config or ChannelConfig()
    addr = target or cfg.target

    options: list[tuple[str, Any]] = [
        ("grpc.max_send_message_length", cfg.max_send_message_length),
        ("grpc.max_receive_message_length", cfg.max_receive_message_length),
        ("grpc.max_concurrent_streams", cfg.max_concurrent_streams),
    ]

    return grpc.aio.insecure_channel(addr, options=options)
