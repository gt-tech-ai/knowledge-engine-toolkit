"""``CachedEmbedding`` — reuse a text's vector across calls, through a ``Cache``.

The key is the model, a caller-set ``version`` (bump it to invalidate every vector, e.g.
after a prefix change), the input type and the text's SHA-256, so two models or two input
types never share a vector and the cache never holds the text itself. A batch looks every
key up at once, embeds only the misses (each distinct text once, so exact-duplicate chunks
are embedded once) and stores them with ``ttl_seconds``. Vectors are stored as float32,
the precision embedding models produce. A hit costs no tokens (its ``token_count`` is 0).
Hits and misses are counted in ``embedding_cache_requests_total{result}`` when a metrics
provider is wired.
"""

from __future__ import annotations

import asyncio
import hashlib
from array import array
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.embedding import EmbeddingProvider, EmbeddingResult
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.cache import Cache
    from techai_webutils.core.interfaces.embedding import InputType
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider


@final
class CachedEmbedding(DelegatingAsyncResource[EmbeddingProvider], EmbeddingProvider):
    """Embeds only the texts the cache does not already hold a vector for."""

    def __init__(
        self,
        inner: EmbeddingProvider,
        cache: Cache,
        *,
        ttl_seconds: int,
        version: str,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Cache ``inner``'s vectors in ``cache`` for ``ttl_seconds`` under ``version``.

        Args:
            inner: The provider whose vectors are cached.
            cache: Where the vectors are stored.
            ttl_seconds: How long a vector lives.
            version: Part of every key, so a new value invalidates the cache.
            metrics: Where the hit and miss counter is created.

        """
        super().__init__(inner)
        self._cache = cache
        self._ttl = ttl_seconds
        self._version = version
        self._requests: MetricCounter | None = None
        if metrics is not None:
            self._requests = metrics.counter(
                "embedding_cache_requests_total",
                "Embedding cache lookups, by result (hit or miss).",
                ["result"],
            )

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Return ``text``'s cached vector, or embed and cache it.

        Args:
            text: The text to embed.
            input_type: Whether ``text`` is a stored document or a search query;
                asymmetric models embed the two differently.

        Returns:
            ``text``'s cached vector, or embed and cache it.

        """
        (result,) = await self.embed_batch([text], input_type=input_type)
        return result

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Return each text's vector, embedding each distinct uncached text once.

        Args:
            texts: The texts to embed.
            input_type: Whether the texts are stored documents or search queries.

        Returns:
            Each text's vector, embedding each distinct uncached text once.

        """
        keys = [self._key(text, input_type) for text in texts]
        unique = list(dict.fromkeys(keys))
        stored = await asyncio.gather(*(self._cache.get(k) for k in unique))
        found = {
            k: self._decode(v)
            for k, v in zip(unique, stored, strict=True)
            if v is not None
        }
        missing = [k for k in unique if k not in found]
        self._count("hit", len(texts) - sum(1 for k in keys if k in missing))
        self._count("miss", sum(1 for k in keys if k in missing))
        if missing:
            text_of = dict(zip(keys, texts, strict=True))
            fresh = await self._inner.embed_batch(
                [text_of[k] for k in missing], input_type=input_type
            )
            _ = await asyncio.gather(
                *(
                    self._cache.set(k, self._encode(r), ttl_seconds=self._ttl)
                    for k, r in zip(missing, fresh, strict=True)
                )
            )
            found.update(zip(missing, fresh, strict=True))
        return [found[k] for k in keys]

    @override
    def dimension(self) -> int:
        """Return the inner provider's dimension.

        Returns:
            The inner provider's dimension.

        """
        return self._inner.dimension()

    @override
    def model_name(self) -> str:
        """Return the inner provider's model.

        Returns:
            The inner provider's model.

        """
        return self._inner.model_name()

    def _key(self, text: str, input_type: InputType) -> str:
        """Return the cache key of ``text`` embedded as ``input_type``.

        Args:
            text: The embedded text.
            input_type: Whether it is a query or a document.

        Returns:
            The cache key of ``text`` embedded as ``input_type``.

        """
        digest = hashlib.sha256(text.encode()).hexdigest()
        return f"emb:{self._inner.model_name()}:{self._version}:{input_type}:{digest}"

    def _decode(self, value: bytes) -> EmbeddingResult:
        """Return the cached vector as a result (a hit costs no tokens).

        Args:
            value: The cached bytes.

        Returns:
            The cached vector as a result (a hit costs no tokens).

        """
        vector = array("f")
        vector.frombytes(value)
        return EmbeddingResult(vector.tolist(), self._inner.model_name(), 0)

    @staticmethod
    def _encode(result: EmbeddingResult) -> bytes:
        """Return ``result``'s vector as float32 bytes.

        Args:
            result: The vector to cache.

        Returns:
            ``result``'s vector as float32 bytes.

        """
        return array("f", result.embedding).tobytes()

    def _count(self, result: str, amount: int) -> None:
        """Count ``amount`` lookups with ``result``.

        Args:
            result: ``hit`` or ``miss``.
            amount: How many lookups.

        """
        if self._requests is not None and amount:
            self._requests.inc(amount, result=result)
