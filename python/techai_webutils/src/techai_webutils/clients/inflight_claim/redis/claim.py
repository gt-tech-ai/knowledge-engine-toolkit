"""``RedisInflightClaim``: a claim is a Redis key set with ``NX`` and an expiry.

``claim`` is ``SET <prefix>:<key> <token> NX PX <ttl>``: only one caller sets the key, and it
expires with the message lease. ``release`` deletes the key only while it still holds this
caller's token (one Lua script), so a caller whose claim lapsed never frees a newer holder's.
A Redis error never stops the work: ``claim`` grants it (the duplicate run is today's behaviour,
and stages are idempotent) and ``release`` only logs, since the key expires anyway.
"""

from __future__ import annotations

import logging
import uuid
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.inflight_claim import InflightClaim

if TYPE_CHECKING:
    from redis.asyncio import Redis

logger = logging.getLogger(__name__)

_RELEASE = """
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
end
return 0
"""
"""Delete the key only when it still holds the caller's token."""


class RedisInflightClaim(InflightClaim):
    """Claims keys in Redis, shared by every consumer on the same server."""

    def __init__(self, redis: Redis, prefix: str) -> None:
        """Claim keys under ``prefix`` on the app's async ``redis`` client."""
        self._redis = redis
        self._prefix = prefix
        self._release = redis.register_script(_RELEASE)
        self._tokens: dict[str, str] = {}

    @override
    async def claim(self, key: str, ttl_seconds: float) -> bool:
        """Set the key if absent, expiring after ``ttl_seconds``; a Redis error grants the claim."""
        token = uuid.uuid4().hex
        try:
            won = await self._redis.set(
                self._name(key), token, nx=True, px=max(1, int(ttl_seconds * 1000))
            )
        except Exception:
            logger.warning(
                "in-flight claim of %s failed; running anyway", key, exc_info=True
            )
            return True
        if won:
            self._tokens[key] = token
        return bool(won)

    @override
    async def release(self, key: str) -> None:
        """Delete the key if this caller still holds it; a Redis error is only logged."""
        token = self._tokens.pop(key, None)
        if token is None:
            return
        try:
            await self._release(keys=[self._name(key)], args=[token])
        except Exception:
            logger.warning(
                "in-flight release of %s failed; it expires", key, exc_info=True
            )

    def _name(self, key: str) -> str:
        """Return the Redis key of claim ``key``."""
        return f"{self._prefix}:{key}"
