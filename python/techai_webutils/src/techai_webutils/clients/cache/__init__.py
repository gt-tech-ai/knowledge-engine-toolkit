"""Cache client tier — config-selected backends (redis/local/null) + decorators.

Mirrors Go's ``clients/cache``: ``builder.py`` selects a backend by ``CacheKind`` and returns the
core ``Cache`` interface; each backend lives in its own subpackage (``redis``/``local``/``null``);
``decorators.py`` wraps a cache with logging + metrics. Relocated from ``foundation/cache`` so the
concrete backends sit at the clients tier (L2, external-system integration), matching Go.
"""

from techai_webutils.clients.cache.builder import (
    CacheConfig,
    CacheKind,
    default_config,
    new_cache_from_config,
)
from techai_webutils.clients.cache.decorators import CacheBuilder
from techai_webutils.clients.cache.envelope import CacheEnvelope, decode, encode
from techai_webutils.clients.cache.local import LocalCache
from techai_webutils.clients.cache.null import NullCache
from techai_webutils.clients.cache.redis import RedisCache
from techai_webutils.clients.cache.types import FailureMode

__all__ = [
    "CacheBuilder",
    "CacheConfig",
    "CacheEnvelope",
    "CacheKind",
    "FailureMode",
    "LocalCache",
    "NullCache",
    "RedisCache",
    "decode",
    "default_config",
    "encode",
    "new_cache_from_config",
]
