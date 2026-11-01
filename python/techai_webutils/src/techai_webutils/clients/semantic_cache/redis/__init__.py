"""The ``redis`` semantic cache: KV payloads plus a RediSearch cosine KNN index."""

from techai_webutils.clients.semantic_cache.redis.cache import RedisSemanticCache

__all__ = ["RedisSemanticCache"]
