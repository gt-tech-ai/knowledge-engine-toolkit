"""RPC client builder — ``new_rpc_channel_from_config`` (shape; mirrors Go ``rpc.NewFromConfig``).

The tier-root factory: selects an RPC transport backend by ``RpcKind`` (gRPC today) and returns the
``grpc.aio.Channel`` that generated stubs are built on. The concrete channel factory lives in the
``grpc/`` subpackage. Unknown kinds fail loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.rpc.grpc.client import ChannelConfig, create_channel
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    import grpc


class RpcKind(StrEnum):
    """Which RPC transport backend to build."""

    GRPC = "grpc"
    """gRPC over HTTP/2 (the only transport today)."""


@dataclass(frozen=True, slots=True)
class RpcConfig:
    """RPC client configuration."""

    kind: RpcKind = RpcKind.GRPC
    """Selects the transport backend."""
    channel: ChannelConfig = field(default_factory=ChannelConfig)
    """gRPC channel options (dial target, message sizes, concurrent streams)."""


def new_rpc_channel_from_config(config: RpcConfig) -> grpc.aio.Channel:
    """Return the ``grpc.aio.Channel`` selected by ``config.kind`` (gRPC today)."""
    if config.kind is RpcKind.GRPC:
        return create_channel(config=config.channel)
    msg = f"unknown rpc kind: {config.kind!r}"
    raise AppValueError(msg)
