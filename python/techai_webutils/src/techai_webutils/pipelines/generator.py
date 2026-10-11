"""Streaming response generator: retrieved passages + query -> answer with inline citations.

The generate step of the three-step RAG pattern. The **prompt is dependency-injected** (a
``GenerationPrompt`` that numbers the context passages + adds the citation instruction) so this generic
pipeline carries no product-specific prompt text — the consuming app supplies the prompt strategy.
Implements ``core.interfaces.retrieval.AnswerGenerator``; streams token-by-token via the injected LLM.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.retrieval import AnswerGenerator

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Sequence

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMProvider
    from techai_webutils.core.interfaces.retrieval import HistoryTurn, RetrievalResult


class GenerationPrompt(Protocol):
    """Builds the generation LLM messages from the query + passages + history (DI'd by the consumer)."""

    def messages(
        self, query: str, passages: list[RetrievalResult], history: Sequence[HistoryTurn]
    ) -> list[LLMMessage]:
        """Return the LLM messages instructing the model to answer ``query`` from ``passages``.

        ``history`` is rendered as non-citable context; passages remain the only citable source.
        """
        ...


class ResponseGenerator(AnswerGenerator):
    """Generates a cited answer from a query + retrieved passages via an LLM + an injected prompt."""

    def __init__(
        self, llm: LLMProvider, prompt: GenerationPrompt, config: LLMConfig | None = None
    ) -> None:
        """Bind the LLM provider, the consumer-supplied generation prompt, and the per-call decoding config.

        ``config`` carries the max_tokens/temperature/top_p the composition root resolves from the
        consumer's config; ``None`` lets the provider apply its conservative defaults.
        """
        self._llm = llm
        self._prompt = prompt
        self._config = config

    async def stream(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> AsyncIterator[str]:
        """Stream the generated answer token-by-token (history reaches the prompt as non-citable context)."""
        iterator = await self._llm.stream(
            self._prompt.messages(query, passages, history or ()), self._config
        )
        async for delta in iterator:
            yield delta

    async def complete(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> str:
        """Generate the full answer (non-streaming); history is non-citable context for the prompt."""
        response = await self._llm.complete(
            self._prompt.messages(query, passages, history or ()), self._config
        )
        return response.content
