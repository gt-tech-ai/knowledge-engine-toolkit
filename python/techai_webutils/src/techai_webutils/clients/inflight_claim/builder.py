"""``inflight_claim_from_config``: the config-selected ``InflightClaim``."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.inflight_claim.noop import NoopInflightClaim
from techai_webutils.clients.inflight_claim.redis import RedisInflightClaim
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from redis.asyncio import Redis

    from techai_webutils.core.interfaces.inflight_claim import InflightClaim


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


def inflight_claim_from_config(
    config: InflightClaimConfig, *, redis: Redis | None = None
) -> InflightClaim:
    """Build the ``InflightClaim`` ``config.kind`` selects.

    ``redis`` is the app's async client, required for the ``redis`` kind.

    Raises:
        AppValueError: An unknown kind, or ``redis`` without a client (``INVALID_INPUT``).

    """
    if config.kind == InflightClaimKind.NOOP:
        return NoopInflightClaim()
    if config.kind == InflightClaimKind.REDIS:
        if redis is None:
            msg = "the redis in-flight claim needs a redis client"
            raise AppValueError(msg)
        return RedisInflightClaim(redis, config.prefix)
    msg = f"unknown in-flight claim kind: {config.kind!r}"
    raise AppValueError(msg)
