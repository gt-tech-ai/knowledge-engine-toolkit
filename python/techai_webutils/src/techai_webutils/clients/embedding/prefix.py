"""``PrefixedEmbedding`` — the per-model query and document prefixes of an asymmetric embedder.

Asymmetric models are trained with a task prefix on each text: nomic-embed-text expects
``search_query: `` on a query and ``search_document: `` on a passage, E5 ``query: `` and
``passage: ``. Embedding both sides bare (today's behaviour) still works but ranks worse. The
factory wraps a provider in this decorator only when ``EmbeddingConfig`` sets a prefix, so a
config with none (the default, and every symmetric model such as Titan) embeds exactly as before.

Changing a prefix changes every document vector: re-embed the corpus (a new index) when you turn
it on, or queries and documents land in different spaces.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.embedding import EmbeddingProvider
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingResult, InputType


class PrefixedEmbedding(DelegatingAsyncResource[EmbeddingProvider], EmbeddingProvider):
    """Prepends the model's query or document prefix before the inner provider embeds a text."""

    def __init__(
        self, inner: EmbeddingProvider, *, query_prefix: str, document_prefix: str
    ) -> None:
        """Wrap ``inner``; ``query_prefix`` and ``document_prefix`` go before each text of that type."""
        self._inner = inner
        self._prefixes: dict[InputType, str] = {
            "query": query_prefix,
            "document": document_prefix,
        }

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Embed ``text`` behind the prefix for ``input_type``."""
        return await self._inner.embed(
            self._prefixes[input_type] + text, input_type=input_type
        )

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Embed each text behind the prefix for ``input_type``, in one inner batch."""
        prefix = self._prefixes[input_type]
        return await self._inner.embed_batch(
            [prefix + text for text in texts], input_type=input_type
        )

    @override
    def dimension(self) -> int:
        """Return the inner provider's dimension."""
        return self._inner.dimension()

    @override
    def model_name(self) -> str:
        """Return the inner provider's model."""
        return self._inner.model_name()
