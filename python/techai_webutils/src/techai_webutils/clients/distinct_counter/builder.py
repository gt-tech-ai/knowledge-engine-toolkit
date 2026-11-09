"""``distinct_counter_from_config``: the config-selected ``DistinctCounter``.

``noop`` (the default) counts nothing and costs nothing. ``redis`` keeps one HyperLogLog
per key (``PFADD`` then ``PFCOUNT``, about 0.8% standard error, at most 12 KB a key and
far less while a key is small), each expiring ``ttl_seconds`` after its last observation,
so a key nobody observes stops costing memory. One pipeline carries a call's keys in a
single round trip.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, override

from redis.asyncio import Redis

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.distinct_counter import DistinctCounter

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider

logger = logging.getLogger(__name__)


class DistinctCounterKind(StrEnum):
    """Which distinct counter to build."""

    NOOP = "noop"
    """Counts nothing: every count is 0."""
    REDIS = "redis"
    """A Redis HyperLogLog per key, shared by every consumer."""


@dataclass(frozen=True, slots=True)
class DistinctCounterConfig:
    """Selects the distinct counter."""

    kind: DistinctCounterKind = DistinctCounterKind.NOOP
    """The counter to build; ``noop`` by default."""
    prefix: str = "distinct"
    """``redis``: the key prefix of every counter."""
    ttl_seconds: int = 30 * 24 * 3600
    """``redis``: a key expires this long after its last observation (30 days)."""
    host: str = ""
    """``redis``: the host the factory opens its own client on when none is passed."""
    port: int = 6379
    """``redis``: the port of a client the factory opens."""
    password: str = ""
    """``redis``: the password of a client the factory opens (empty: none)."""
    db: int = 0
    """``redis``: the logical database of a client the factory opens."""
    timeout_seconds: float = 0.5
    """``redis``: the socket and connect timeout of a client the factory opens."""


class NoopDistinctCounter(DistinctCounter):
    """Counts nothing."""

    @override
    async def observe(self, keys: Sequence[str], member: str) -> list[int]:
        """Return a zero per key."""
        del member
        return [0] * len(keys)

    @override
    async def aclose(self) -> None:
        """Hold nothing to release."""


class RedisDistinctCounter(DistinctCounter):
    """Counts distinct members per key with Redis HyperLogLogs."""

    def __init__(
        self,
        redis: Redis,
        config: DistinctCounterConfig,
        *,
        errors: MetricCounter | None = None,
        owned: bool = False,
    ) -> None:
        """Count under ``config.prefix`` on ``redis``.

        Keys expiring after ``config.ttl_seconds``.

        ``errors`` counts each Redis error the counter absorbs; ``owned`` marks a client
        the factory opened, which ``aclose`` closes.
        """
        self._redis = redis
        self._prefix = config.prefix
        self._ttl = config.ttl_seconds
        self._errors = errors
        self._owned = owned

    @override
    async def observe(self, keys: Sequence[str], member: str) -> list[int]:
        """Add ``member`` to each key's HyperLogLog and return the counts.

        Zeros on a Redis error.
        """
        if not keys:
            return []
        pipe = self._redis.pipeline(transaction=False)
        for key in keys:
            name = f"{self._prefix}:{key}"
            pipe.pfadd(name, member)
            pipe.expire(name, self._ttl)
            pipe.pfcount(name)
        try:
            replies = await pipe.execute()
        except Exception:
            logger.warning(
                "distinct count of %d keys failed; counting 0", len(keys), exc_info=True
            )
            if self._errors is not None:
                self._errors.inc(op="observe")
            return [0] * len(keys)
        return [int(replies[i]) for i in range(2, len(replies), 3)]

    @override
    async def aclose(self) -> None:
        """Close the Redis client when the factory opened it.

        An injected one is the app's.
        """
        if self._owned:
            await self._redis.aclose()


def distinct_counter_from_config(
    config: DistinctCounterConfig,
    *,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
) -> DistinctCounter:
    """Build the ``DistinctCounter`` ``config.kind`` selects.

    ``redis`` is the app's async client; without one, the ``redis`` kind opens its own on
    ``config.host`` (bounded by ``config.timeout_seconds``), which ``aclose`` closes.
    ``metrics`` counts the Redis errors the counter absorbs
    (``distinct_counter_errors_total{op}``).

    Raises:
        AppValueError: An unknown kind, or ``redis`` with neither a client nor a host, or
            a ``ttl_seconds`` below 1 (``INVALID_INPUT``).

    """
    if config.kind == DistinctCounterKind.NOOP:
        return NoopDistinctCounter()
    if config.kind == DistinctCounterKind.REDIS:
        if (redis is None and not config.host) or config.ttl_seconds < 1:
            msg = (
                "the redis distinct counter needs a redis client or host and "
                "ttl_seconds >= 1"
            )
            raise AppValueError(msg)
        errors = None
        if metrics is not None:
            errors = metrics.counter(
                "distinct_counter_errors_total",
                "Redis errors a distinct counter absorbed (the counts read 0).",
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
        return RedisDistinctCounter(client, config, errors=errors, owned=redis is None)
    msg = f"unknown distinct counter kind: {config.kind!r}"
    raise AppValueError(msg)
