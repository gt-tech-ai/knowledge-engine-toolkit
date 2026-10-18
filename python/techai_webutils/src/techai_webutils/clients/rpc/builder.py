"""RPC client builder — ``new_rpc_channel_from_config`` (shape; mirrors Go ``rpc.NewFromConfig``).

The tier-root factory: selects an RPC transport backend by ``RpcKind`` (gRPC today) and returns the
``grpc.aio.Channel`` that generated stubs are built on, every call run through the client tier. The concrete channel factory lives in the
``grpc/`` subpackage. Unknown kinds fail loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.client import ClientBase
from techai_webutils.clients.rpc.grpc.client import ChannelConfig, create_channel
from techai_webutils.clients.rpc.grpc.interceptors.client import client_interceptors
from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.decorate import Decorations

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
    name: str = "rpc"
    """The client's name in the decoration config's clients tier (and its metric, span and log
    label)."""
    service_token: str = ""
    """The caller's service-to-service bearer token, sent on every call; empty sends none (a
    local-dev server admits the call). Set it from secrets."""
    channel: ChannelConfig = field(default_factory=ChannelConfig)
    """gRPC channel options (dial target, message sizes, concurrent streams)."""


def new_rpc_channel_from_config(
    config: RpcConfig, decorations: Decorations | None = None
) -> grpc.aio.Channel:
    """Return the ``grpc.aio.Channel`` selected by ``config.kind`` (gRPC today), without I/O.

    Every call runs through the clients tier under ``config.name`` (see
    ``client_interceptors``), its layers resolved from ``decorations``. Without
    ``decorations`` the tier has no layers: each call is undecorated but still carries the
    trace context and ``config.service_token``.
    """
    if config.kind is RpcKind.GRPC:
        base = ClientBase.wire(decorations or Decorations(), config.name or "rpc")
        return create_channel(
            config=config.channel,
            interceptors=client_interceptors(base, config.service_token),
        )
    msg = f"unknown rpc kind: {config.kind!r}"
    raise AppValueError(msg)
