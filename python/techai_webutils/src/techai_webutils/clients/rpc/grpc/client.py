"""Async gRPC client channel factory.

Creates configured ``grpc.aio`` channels with a 16 MB max message size and a bounded
max-concurrent-streams. (Channel pooling / idle timeout are not configured here — a single
``grpc.aio`` channel already multiplexes concurrent RPCs.)
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

import grpc

from techai_webutils.core.errors import InvalidInputError

if TYPE_CHECKING:
    from collections.abc import Sequence

_16MB = 16 * 1024 * 1024
"""Default gRPC max send/receive message size, in bytes (16 MiB)."""


class LoadBalancing(StrEnum):
    """Client-side gRPC load-balancing policy."""

    PICK_FIRST = "pick_first"
    """Pin every call to the first resolved backend (gRPC's default)."""
    ROUND_ROBIN = "round_robin"
    """Rotate calls across every resolved backend, re-resolving as the set changes."""


_ROUND_ROBIN_SERVICE_CONFIG = '{"loadBalancingConfig":[{"round_robin":{}}]}'
"""The gRPC service config that selects the ``round_robin`` LB policy."""


@dataclass
class ChannelConfig:
    """gRPC channel configuration (the options actually applied by ``create_channel``)."""

    target: str = "localhost:50051"
    """Default dial target when ``create_channel`` is called without an explicit
    target."""
    max_send_message_length: int = _16MB
    """Max outbound message size in bytes."""
    max_receive_message_length: int = _16MB
    """Max inbound message size in bytes."""
    max_concurrent_streams: int = 100
    """Cap on concurrent HTTP/2 streams multiplexed over the channel."""
    load_balancing: LoadBalancing = LoadBalancing.PICK_FIRST
    """The client-side load-balancing policy over the resolved backends."""


def create_channel(
    target: str | None = None,
    config: ChannelConfig | None = None,
    interceptors: Sequence[grpc.aio.ClientInterceptor] = (),
) -> grpc.aio.Channel:
    """Create a configured async ``grpc.aio`` channel.

    ``target`` overrides ``config.target`` when provided; ``config`` defaults to
    ``ChannelConfig()``. Returns an insecure channel carrying the message-size +
    concurrent-stream options, every call run through ``interceptors`` (first outermost).

    Args:
        target: The server address; ``None`` uses the config's.
        config: The channel settings; ``None`` uses the defaults.
        interceptors: Run around every call, outermost first.

    Returns:
        A configured async ``grpc.aio`` channel.

    """
    cfg = config or ChannelConfig()
    addr = target or cfg.target

    options: list[tuple[str, int | str]] = [
        ("grpc.max_send_message_length", cfg.max_send_message_length),
        ("grpc.max_receive_message_length", cfg.max_receive_message_length),
        ("grpc.max_concurrent_streams", cfg.max_concurrent_streams),
    ]
    options.extend(_load_balancing_options(cfg.load_balancing))

    return grpc.aio.insecure_channel(
        addr, options=options, interceptors=list(interceptors) or None
    )


def _load_balancing_options(policy: LoadBalancing) -> list[tuple[str, int | str]]:
    """Translate a load-balancing policy into its gRPC channel options.

    ``pick_first`` is gRPC's default and needs no option; ``round_robin`` is selected by a
    service-config channel arg. An unrecognised policy fails loudly.

    Args:
        policy: The client-side load-balancing policy.

    Returns:
        The channel options that select ``policy`` (empty for ``pick_first``).

    Raises:
        InvalidInputError: The policy is not a recognised ``LoadBalancing`` value.

    """
    if policy == LoadBalancing.PICK_FIRST:
        return []
    if policy == LoadBalancing.ROUND_ROBIN:
        return [("grpc.service_config", _ROUND_ROBIN_SERVICE_CONFIG)]
    msg = f"grpc client: unknown load_balancing {policy!r}"
    raise InvalidInputError(msg)
