"""The ``redis`` semantic cache: KV payloads plus a cosine KNN index.

The index is RediSearch or Valkey Search.
"""

from techai_webutils.clients.semantic_cache.redis.cache import RedisSemanticCache

__all__ = ["RedisSemanticCache"]
