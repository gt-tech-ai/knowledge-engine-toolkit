"""Config-selected ``TokenLedger`` factory (the ``clients/llm/builder.py`` pattern).

``TokenLedgerConfig.kind`` selects a built-in backend — ``stub`` (allow-all, the default) or
``redis`` (the hot-path counters + usage stream) — or a consumer-supplied kind from the injected
``backends`` mapping (e.g. a remote durable ledger the KE must not import). An unknown kind raises a
coded ``AppError(INVALID_INPUT)``; the Redis backend is imported lazily.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from types import MappingProxyType
from typing import TYPE_CHECKING

from techai_webutils.clients.token_ledger.stub import StubTokenLedger
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.token_ledger import Period

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from redis.asyncio import Redis

    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.token_ledger import TokenLedger


class TokenLedgerKind(StrEnum):
    """The built-in token ledger backends."""

    STUB = "stub"
    """Allow-all, record-nothing (dev/test; zero infrastructure)."""
    REDIS = "redis"
    """Redis counters + a bounded usage stream (the hot path)."""


@dataclass(frozen=True, slots=True)
class TokenLedgerConfig:
    """Token ledger configuration."""

    kind: str = TokenLedgerKind.STUB
    """A ``TokenLedgerKind`` value or a key of the injected ``backends``."""
    window: Period = Period.MONTHLY
    """The budget window the hot-path counters cover."""
    redis_prefix: str = "token_ledger"
    """Key prefix of the Redis counters (``<prefix>:{org}:{workspace}:{window}``)."""
    stream: str = "token_ledger:usage"
    """The Redis stream each recorded usage is appended to."""
    stream_maxlen: int = 1_000_000
    """Approximate cap on the usage stream length (``XADD MAXLEN ~``)."""


def token_ledger_from_config(
    config: TokenLedgerConfig,
    *,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
    backends: Mapping[
        str, Callable[[TokenLedgerConfig], TokenLedger]
    ] = MappingProxyType({}),
) -> TokenLedger:
    """Build the ``TokenLedger`` selected by ``config.kind``.

    Args:
        config: The ledger configuration.
        redis: The async Redis client; required for the ``redis`` kind (the app's own client).
        metrics: Provides ``token_ledger_record_failed_total`` for the ``redis`` kind.
        backends: Factories for consumer-supplied kinds, keyed by kind; a built-in kind name is
            never looked up here.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown kind or a ``redis`` kind without a client.

    """
    if config.kind == TokenLedgerKind.STUB:
        return StubTokenLedger()
    if config.kind == TokenLedgerKind.REDIS:
        if redis is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "redis token ledger requires a Redis client"
            )
        from techai_webutils.clients.token_ledger.redis import RedisTokenLedger  # noqa: PLC0415

        return RedisTokenLedger(redis, config, metrics=metrics)
    factory = backends.get(config.kind)
    if factory is None:
        raise AppError(
            ErrorCode.INVALID_INPUT, f"unknown token ledger kind: {config.kind!r}"
        )
    return factory(config)
