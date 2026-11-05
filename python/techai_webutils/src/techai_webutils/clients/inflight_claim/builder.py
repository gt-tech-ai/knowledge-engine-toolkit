"""``inflight_claim_from_config``: the config-selected ``InflightClaim``."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from redis.asyncio import Redis

from techai_webutils.clients.inflight_claim.noop import NoopInflightClaim
from techai_webutils.clients.inflight_claim.redis import RedisInflightClaim
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.inflight_claim import InflightClaim
    from techai_webutils.core.interfaces.metrics import MetricsProvider


class InflightClaimKind(StrEnum):
    """Which in-flight claim to build."""

    NOOP = "noop"
    """Every claim is granted: concurrent duplicates all run (today's behaviour)."""
    REDIS = "redis"
    """Claims are Redis keys shared by every consumer."""


@dataclass(frozen=True)
class InflightClaimConfig:
    """Selects the in-flight claim."""

    kind: InflightClaimKind = InflightClaimKind.NOOP
    """The claim to build; ``noop`` by default."""
    prefix: str = "inflight"
    """The Redis key prefix of every claim (``redis``)."""
    host: str = ""
    """The Redis host the factory opens its own client on when none is passed (``redis``)."""
    port: int = 6379
    """The Redis port of a client the factory opens."""
    password: str = ""
    """The Redis password of a client the factory opens (empty: none)."""
    db: int = 0
    """The Redis logical database of a client the factory opens."""
    timeout_seconds: float = 0.5
    """The socket and connect timeout of a client the factory opens (``redis``)."""


def inflight_claim_from_config(
    config: InflightClaimConfig,
    *,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
) -> InflightClaim:
    """Build the ``InflightClaim`` ``config.kind`` selects.

    ``redis`` is the app's async client; without one, the ``redis`` kind opens its own on
    ``config.host`` (bounded by ``config.timeout_seconds``), which the claim's ``aclose``
    closes. ``metrics`` counts the Redis errors the claim absorbs
    (``inflight_claim_errors_total{op}``).

    Raises:
        AppValueError: An unknown kind, or ``redis`` with neither a client nor a host
            (``INVALID_INPUT``).

    """
    if config.kind == InflightClaimKind.NOOP:
        return NoopInflightClaim()
    if config.kind == InflightClaimKind.REDIS:
        if redis is None and not config.host:
            msg = "the redis in-flight claim needs a redis client or host"
            raise AppValueError(msg)
        errors = None
        if metrics is not None:
            errors = metrics.counter(
                "inflight_claim_errors_total",
                "Redis errors an in-flight claim absorbed (the work ran anyway).",
                ["op"],
            )
        client = redis or Redis(
            host=config.host,
            port=config.port,
            password=config.password or None,
            db=config.db,
            socket_timeout=config.timeout_seconds,
            socket_connect_timeout=config.timeout_seconds,
        )
        return RedisInflightClaim(
            client, config.prefix, errors=errors, owned=redis is None
        )
    msg = f"unknown in-flight claim kind: {config.kind!r}"
    raise AppValueError(msg)
