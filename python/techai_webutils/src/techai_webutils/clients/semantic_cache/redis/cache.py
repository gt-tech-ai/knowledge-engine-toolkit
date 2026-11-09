"""``RedisSemanticCache`` — payloads in the injected KV ``Cache``, similarity in a search index.

The index runs on either search engine: RediSearch (Redis Stack) or Valkey Search (Valkey 9, e.g.
ElastiCache for Valkey). Where they differ the code uses only what both accept: no ``SORTBY`` on the
KNN score, no ``FT.DROPINDEX … DD``, and an ``FT.SEARCH`` reply read in either shape (a RESP3 map
from RediSearch, the RESP2 array from Valkey Search on the same connection).

Layout (``<p>`` is ``SemanticCacheConfig.index_prefix``; ``<ws>`` the namespace's workspace;
``<tag>`` its ``CacheNamespace.tag()``; ``<h>`` the sha256 of the normalized query):

- **Payload** — ``<p>:p:<ws>:<tag>:<h>`` in the KV cache, with the store's TTL.
- **Index document** — a hash ``<p>:d:<ws>:<tag>:<h>`` with ``ns`` (the tag), ``key`` (the payload
  key), ``query`` and ``embedding`` (float32 little-endian), expiring with the payload.
- **Index** — ``<p>:idx`` over the ``<p>:d:`` prefix: ``ns`` TAG + ``embedding`` HNSW COSINE of the
  embedder's dimension, created lazily (idempotently) on first use.

``exact`` reads the normalized repeat's payload (no embedding); ``nearest`` runs the top-1 KNN
within the namespace's tag and returns the neighbour with similarity ``1 - distance`` when its
payload still exists, leaving any threshold to the caller (``lookup`` and the match tiers). Every backend error is logged, counted (``semantic_cache_errors_total{op}``) and turned
into a miss or a no-op. The workspace leads the key so ``invalidate_prefix(<workspace>)`` evicts
one tenant.
"""

from __future__ import annotations

import hashlib
import re
import struct
from typing import TYPE_CHECKING, Any, cast, override

from redis.commands.search.field import TagField, VectorField
from redis.commands.search.index_definition import IndexDefinition, IndexType
from redis.exceptions import ResponseError

from techai_webutils.core.interfaces.semantic_cache import CachedAnswer, SemanticCache
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Awaitable, Callable

    from redis.asyncio import Redis

    from techai_webutils.clients.semantic_cache.builder import SemanticCacheConfig
    from techai_webutils.core.interfaces.cache import Cache
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider
    from techai_webutils.core.interfaces.semantic_cache import CacheNamespace

_GLOB = re.compile(r"([*?\[\]\\])")
"""Characters escaped in a ``SCAN MATCH`` pattern built from a caller's prefix."""

_TRAILING_PUNCTUATION = ".?!"
"""Characters ``strip_trailing_punctuation`` removes from the end of a normalized query."""


def _text(value: object) -> str:
    """Return a Redis reply value as ``str`` (decoded when the client returns bytes)."""
    return value.decode() if isinstance(value, bytes) else str(value)


def _first_document(reply: object) -> dict[str, str] | None:
    """Return the first ``FT.SEARCH`` document's fields as text, or ``None`` when there is none.

    Reads both reply shapes: the RESP3 map (``{results: [{extra_attributes: {…}}]}``, RediSearch)
    and the RESP2 array (``[total, id, [field, value, …], …]``, Valkey Search).
    """
    if isinstance(reply, dict):
        results = cast(
            "list[dict[Any, Any]]", _field(cast("dict[Any, Any]", reply), "results") or []
        )
        if not results:
            return None
        attributes = cast("dict[Any, Any]", _field(results[0], "extra_attributes") or {})
        return {_text(k): _text(v) for k, v in attributes.items()}
    items = cast("list[Any]", reply)
    if len(items) < 3:  # noqa: PLR2004 - [total, id, fields] is the shortest reply with a hit
        return None
    fields = cast("list[Any]", items[2])
    return {_text(fields[i]): _text(fields[i + 1]) for i in range(0, len(fields) - 1, 2)}


def _field(mapping: dict[Any, Any], name: str) -> object:
    """Return ``mapping[name]`` whether the client decoded keys to ``str`` or left them ``bytes``."""
    return mapping.get(name, mapping.get(name.encode()))


class RedisSemanticCache(SemanticCache):
    """A semantic cache on the app's Redis or Valkey: KV payloads plus a search-module KNN index."""

    def __init__(
        self,
        cache: Cache,
        embedding: EmbeddingProvider,
        redis: Redis,
        config: SemanticCacheConfig,
        *,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Bind the collaborators; the index is created on first use, never here."""
        self._cache = cache
        self._embedding = embedding
        self._redis = redis
        self._config = config
        self._prefix = config.index_prefix
        self._index = f"{config.index_prefix}:idx"
        self._index_ready = False
        self._logger = get_logger("semantic_cache")
        self._errors: MetricCounter | None = None
        if metrics is not None:
            self._errors = metrics.counter(
                "semantic_cache_errors_total",
                "Semantic cache backend errors, served as a miss or a no-op.",
                ["op"],
            )

    def _normalize(self, query: str) -> str:
        """Return ``query`` normalized for keying.

        Lower-cased and whitespace-collapsed when ``normalize`` is on; trailing punctuation
        removed when ``strip_trailing_punctuation`` is on.
        """
        text = " ".join(query.lower().split()) if self._config.normalize else query
        if self._config.strip_trailing_punctuation:
            text = text.rstrip(_TRAILING_PUNCTUATION).rstrip()
        return text

    def _suffix(self, query: str, namespace: CacheNamespace) -> str:
        """Return the ``<ws>:<tag>:<h>`` key suffix shared by a payload and its document."""
        digest = hashlib.sha256(self._normalize(query).encode()).hexdigest()
        return f"{namespace.workspace_id}:{namespace.tag()}:{digest}"

    def _search(self) -> Any:  # noqa: ANN401 - redis-py's search handle is loosely typed
        """Return the search handle for this cache's index (index create and drop)."""
        return self._redis.ft(self._index)

    def _failed(self, op: str, exc: Exception) -> None:
        """Log and count a swallowed backend error."""
        self._logger.warning(
            "semantic cache backend error (fail-open)",
            extra={"op": op, "error": str(exc)},
        )
        if self._errors is not None:
            self._errors.inc(op=op)

    async def _ensure_index(self) -> None:
        """Create the KNN index once; an existing index counts as created."""
        if self._index_ready:
            return
        vector = VectorField(
            "embedding",
            "HNSW",
            {
                "TYPE": "FLOAT32",
                "DIM": self._embedding.dimension(),
                "DISTANCE_METRIC": "COSINE",
            },
        )
        definition = IndexDefinition(
            prefix=[f"{self._prefix}:d:"], index_type=IndexType.HASH
        )
        try:
            await self._search().create_index(
                [TagField("ns"), vector], definition=definition
            )
        except ResponseError as exc:
            if "already exists" not in str(exc).lower():
                raise
        self._index_ready = True

    async def _vector(self, query: str) -> bytes:
        """Return ``query``'s embedding packed as little-endian float32."""
        values = (await self._embedding.embed(query)).embedding
        return struct.pack(f"<{len(values)}f", *values)

    @override
    async def exact(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Return the normalized repeat's payload without embedding; fail-open."""
        try:
            payload = await self._cache.get(
                f"{self._prefix}:p:{self._suffix(query, namespace)}"
            )
        except Exception as exc:
            self._failed("lookup", exc)
            return None
        if payload is None:
            return None
        return CachedAnswer(
            payload=payload,
            similarity=1.0,
            source_query=query,
            embedding_model=self._embedding.model_name(),
        )

    @override
    async def nearest(self, query: str, namespace: CacheNamespace) -> CachedAnswer | None:
        """Return the namespace's closest neighbour with its similarity, unthresholded; fail-open."""
        try:
            return await self._nearest(query, namespace)
        except Exception as exc:
            self._failed("lookup", exc)
            return None

    async def _nearest(
        self, query: str, namespace: CacheNamespace
    ) -> CachedAnswer | None:
        """Run the namespace-scoped top-1 KNN and read the neighbour's payload."""
        model = self._embedding.model_name()
        await self._ensure_index()
        # Raw FT.SEARCH: redis-py's parser expects the RESP3 map, which Valkey Search never sends.
        # KNN 1 yields at most one neighbour, so there is no SORTBY (Valkey can't sort on the alias).
        execute = cast("Callable[..., Awaitable[object]]", self._redis.execute_command)
        reply = await execute(
            "FT.SEARCH",
            self._index,
            f"(@ns:{{{namespace.tag()}}})=>[KNN 1 @embedding $vec AS dist]",
            "PARAMS",
            "2",
            "vec",
            await self._vector(query),
            "RETURN",
            "3",
            "key",
            "query",
            "dist",
            "DIALECT",
            "2",
        )
        nearest = _first_document(reply)
        if nearest is None:
            return None
        similarity = 1.0 - float(nearest["dist"])
        payload = await self._cache.get(nearest["key"])
        if payload is None:
            return None
        return CachedAnswer(
            payload=payload,
            similarity=similarity,
            source_query=nearest["query"],
            embedding_model=model,
        )

    @override
    async def store(
        self, query: str, payload: bytes, namespace: CacheNamespace, *, ttl_seconds: int
    ) -> None:
        """Write the payload with its TTL and index the query's vector; fail-open."""
        try:
            await self._store(query, payload, namespace, ttl_seconds)
        except Exception as exc:
            self._failed("store", exc)

    async def _store(
        self, query: str, payload: bytes, namespace: CacheNamespace, ttl_seconds: int
    ) -> None:
        """Write the payload, then its index document, both expiring after ``ttl_seconds``."""
        await self._ensure_index()
        suffix = self._suffix(query, namespace)
        payload_key, doc_key = f"{self._prefix}:p:{suffix}", f"{self._prefix}:d:{suffix}"
        await self._cache.set(payload_key, payload, ttl_seconds=ttl_seconds)
        mapping: dict[str, str | bytes] = {
            "ns": namespace.tag(),
            "key": payload_key,
            "query": query,
            "embedding": await self._vector(query),
        }
        await self._redis.hset(doc_key, mapping=mapping)  # pyright: ignore[reportArgumentType]
        await self._redis.expire(doc_key, ttl_seconds)

    async def _unlink_matching(self, pattern: str) -> None:
        """``UNLINK`` every key matching the ``SCAN`` pattern."""
        keys = cast("AsyncIterator[str | bytes]", self._redis.scan_iter(match=pattern))  # pyright: ignore[reportUnknownMemberType]
        async for key in keys:
            await self._redis.unlink(key)

    @override
    async def invalidate_prefix(self, prefix: str) -> None:
        """Evict every payload and document under the ``prefix`` key segment (a workspace).

        The pattern ends at the segment separator, so ``ws-1`` never matches ``ws-10``.
        """
        escaped = _GLOB.sub(r"\\\1", prefix)
        try:
            await self._unlink_matching(f"{self._prefix}:p:{escaped}:*")
            await self._unlink_matching(f"{self._prefix}:d:{escaped}:*")
        except Exception as exc:
            self._failed("invalidate", exc)

    @override
    async def invalidate_all(self) -> None:
        """Drop the index, then every index document and payload key.

        The documents are unlinked here rather than with ``FT.DROPINDEX … DD``, which Valkey Search
        rejects.
        """
        try:
            await self._search().dropindex(delete_documents=False)
            self._index_ready = False
            await self._unlink_matching(f"{self._prefix}:d:*")
            await self._unlink_matching(f"{self._prefix}:p:*")
        except Exception as exc:
            self._failed("invalidate", exc)


_INTERFACE_CHECK: type[SemanticCache] = RedisSemanticCache
"""Type-checker assertion that ``RedisSemanticCache`` satisfies ``SemanticCache``."""
