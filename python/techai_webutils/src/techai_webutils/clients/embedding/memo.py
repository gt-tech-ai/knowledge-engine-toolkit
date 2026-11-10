"""``MemoizedEmbedding`` — embed each text once per request.

The router's classifier and the semantic cache both embed the user's query; wrapping the
embedder in this memo makes the second consumer a hit. Entries are keyed by the inner
model, the input type and the text, so two embedders over different models (or a query and
a document embedding of one text) never share a vector. The memo lives in a
``ContextVar``, so concurrent requests (separate tasks) never share it; the transport edge
calls ``reset_embedding_memo()`` at the start of each request. It is bounded, so a
long-lived context can't grow it without limit.
"""

from __future__ import annotations

from contextvars import ContextVar
from typing import TYPE_CHECKING, Self, final, override

from techai_webutils.core.interfaces.embedding import EmbeddingProvider

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.embedding import EmbeddingResult, InputType

_MAX_ENTRIES = 64
"""Most texts memoized per request (a request embeds a handful)."""

_MEMO: ContextVar[dict[tuple[str, str, str], EmbeddingResult] | None] = ContextVar(
    "embedding_memo", default=None
)
"""The current request's memo: (model, input type, text) → its embedding."""


def reset_embedding_memo() -> None:
    """Start a fresh memo for the current request (call at the transport edge)."""
    _ = _MEMO.set({})


def _memo() -> dict[tuple[str, str, str], EmbeddingResult]:
    """Return the current request's memo, creating it in this context when absent.

    Returns:
        The current request's memo, creating it in this context when absent.

    """
    memo = _MEMO.get()
    if memo is None:
        fresh: dict[tuple[str, str, str], EmbeddingResult] = {}
        _ = _MEMO.set(fresh)
        return fresh
    return memo


@final
class MemoizedEmbedding(EmbeddingProvider):
    """An ``EmbeddingProvider`` that embeds each text once per request."""

    def __init__(self, inner: EmbeddingProvider) -> None:
        """Wrap ``inner``.

        Args:
            inner: The provider whose vectors are memoized.

        """
        self._inner = inner

    @override
    async def embed(
        self, text: str, *, input_type: InputType = "document"
    ) -> EmbeddingResult:
        """Return ``text``'s embedding as ``input_type``.

        This request's memo answers when it holds it.

        Args:
            text: The text to embed.
            input_type: Whether ``text`` is a stored document or a search query;
                asymmetric models embed the two differently.

        Returns:
            ``text``'s embedding as ``input_type``.

        """
        memo = _memo()
        key = (self._inner.model_name(), input_type, text)
        hit = memo.get(key)
        if hit is not None:
            return hit
        result = await self._inner.embed(text, input_type=input_type)
        if len(memo) < _MAX_ENTRIES:
            memo[key] = result
        return result

    @override
    async def embed_batch(
        self, texts: list[str], *, input_type: InputType = "document"
    ) -> list[EmbeddingResult]:
        """Embed ``texts`` through the inner provider (batches are not memoized).

        Args:
            texts: The texts to embed.
            input_type: Whether the texts are stored documents or search queries.

        Returns:
            One embedding per text, in input order.

        """
        return await self._inner.embed_batch(texts, input_type=input_type)

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

    @override
    async def __aenter__(self) -> Self:
        """Enter the inner provider's context.

        Returns:
            This instance.

        """
        _ = await self._inner.__aenter__()
        return self

    @override
    async def __aexit__(self, *exc: object) -> None:
        """Exit the inner provider's context.

        Args:
            *exc: The exception triple, if any.

        """
        _ = await self._inner.__aexit__(None, None, None)
