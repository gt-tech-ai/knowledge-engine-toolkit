"""Config-selected ``TokenLedger`` factory (the ``clients/llm/builder.py`` pattern).

``TokenLedgerConfig.kind`` selects a built-in backend — ``stub`` (allow-all, the default)
or ``redis`` (the hot-path counters + usage stream) — or a consumer-supplied kind from the
injected ``backends`` mapping (e.g. a remote durable ledger the KE must not import). An
unknown kind raises a coded ``AppError(INVALID_INPUT)``; the Redis backend is imported
lazily.
"""

from __future__ import annotations

import importlib
from types import MappingProxyType
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.token_ledger.config import (
    TokenLedgerConfig,
    TokenLedgerKind,
)
from techai_webutils.clients.token_ledger.stub import StubTokenLedger
from techai_webutils.core.errors import AppError, ErrorCode

if TYPE_CHECKING:
    from collections.abc import Callable, Mapping

    from redis.asyncio import Redis

    from techai_webutils.clients.token_ledger.redis import (
        RedisTokenLedger as _LazyRedisTokenLedger,
    )
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.token_ledger import TokenLedger


_DEFAULT_BACKENDS: Mapping[str, Callable[[TokenLedgerConfig], TokenLedger]] = (
    MappingProxyType({})
)
"""The default ``backends``."""


def token_ledger_from_config(
    config: TokenLedgerConfig,
    *,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
    backends: Mapping[
        str, Callable[[TokenLedgerConfig], TokenLedger]
    ] = _DEFAULT_BACKENDS,
) -> TokenLedger:
    """Build the ``TokenLedger`` selected by ``config.kind``.

    Args:
        config: The ledger configuration.
        redis: The async Redis client; required for the ``redis`` kind (the app's own
            client).
        metrics: Provides ``token_ledger_record_failed_total`` for the ``redis`` kind.
        backends: Factories for consumer-supplied kinds, keyed by kind; a built-in kind
            name is never looked up here.

    Returns:
        The ``TokenLedger`` selected by ``config.kind``.

    Raises:
        AppError: ``INVALID_INPUT`` for an unknown kind or a ``redis`` kind without a
            client.

    """
    if config.kind == TokenLedgerKind.STUB:
        return StubTokenLedger()
    if config.kind == TokenLedgerKind.REDIS:
        if redis is None:
            raise AppError(
                ErrorCode.INVALID_INPUT, "redis token ledger requires a Redis client"
            )
        redis_token_ledger_cls = cast(
            "type[_LazyRedisTokenLedger]",
            importlib.import_module(
                "techai_webutils.clients.token_ledger.redis"
            ).RedisTokenLedger,
        )

        return redis_token_ledger_cls(redis, config, metrics=metrics)
    factory = backends.get(config.kind)
    if factory is None:
        raise AppError(
            ErrorCode.INVALID_INPUT, f"unknown token ledger kind: {config.kind!r}"
        )
    return factory(config)
