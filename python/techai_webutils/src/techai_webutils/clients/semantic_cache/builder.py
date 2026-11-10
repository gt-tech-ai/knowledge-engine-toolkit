"""Config-selected ``SemanticCache`` factory.

It follows the ``clients/token_ledger/builder.py`` pattern.

``SemanticCacheConfig.kind`` selects ``stub`` (always miss, the default) or ``redis`` (the
injected KV ``Cache`` for payloads + the ``EmbeddingProvider`` + a search-module KNN index
on the app's Redis or Valkey). Collaborators are injected, never built here. An unknown
kind raises ``AppValueError``; ``redis`` without its collaborators raises
``AppError(INVALID_INPUT)``. The Redis backend is imported lazily.
"""

from __future__ import annotations

import importlib
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.semantic_cache.config import (
    SemanticCacheConfig,
    SemanticCacheKind,
)
from techai_webutils.clients.semantic_cache.stub import StubSemanticCache
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode

if TYPE_CHECKING:
    from redis.asyncio import Redis

    from techai_webutils.clients.semantic_cache.redis import (
        RedisSemanticCache as _LazyRedisSemanticCache,
    )
    from techai_webutils.core.interfaces.cache import Cache
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import SemanticCache


def semantic_cache_from_config(
    config: SemanticCacheConfig,
    *,
    cache: Cache | None = None,
    embedding: EmbeddingProvider | None = None,
    redis: Redis | None = None,
    metrics: MetricsProvider | None = None,
) -> SemanticCache:
    """Build the ``SemanticCache`` selected by ``config.kind``.

    Args:
        config: The semantic cache configuration.
        cache: The KV cache holding payloads (``redis`` kind).
        embedding: The embedder keying similarity (``redis`` kind).
        redis: The app's async Redis client, hosting the KNN index (``redis`` kind).
        metrics: Counts backend errors for the ``redis`` kind.

    Returns:
        The ``SemanticCache`` selected by ``config.kind``.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.
        AppError: ``INVALID_INPUT`` for ``redis`` with a collaborator missing.

    """
    if config.kind == SemanticCacheKind.STUB:
        return StubSemanticCache()
    if config.kind != SemanticCacheKind.REDIS:
        msg = f"unknown semantic cache kind: {config.kind!r}"
        raise AppValueError(msg)
    if cache is None or embedding is None or redis is None:
        raise AppError(
            ErrorCode.INVALID_INPUT,
            "redis semantic cache requires cache, embedding and redis",
        )
    redis_semantic_cache_cls = cast(
        "type[_LazyRedisSemanticCache]",
        importlib.import_module(
            "techai_webutils.clients.semantic_cache.redis"
        ).RedisSemanticCache,
    )

    return redis_semantic_cache_cls(cache, embedding, redis, config, metrics=metrics)
