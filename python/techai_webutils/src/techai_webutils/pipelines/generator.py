"""Streaming response generator: an answer with inline citations.

It answers the query from the retrieved passages.

The generate step of the three-step RAG pattern. The **prompt is dependency-injected** (a
``GenerationPrompt`` that numbers the context passages + adds the citation instruction) so
this generic pipeline carries no product-specific prompt text — the consuming app supplies
the prompt strategy. Implements ``core.interfaces.retrieval.AnswerGenerator``; streams
token-by-token via the injected LLM.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, final, override

from techai_webutils.core.interfaces.retrieval import AnswerGenerator

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Sequence

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMProvider
    from techai_webutils.core.interfaces.retrieval import HistoryTurn, RetrievalResult


class GenerationPrompt(Protocol):
    """Builds the generation LLM messages from the query, passages and history.

    The consumer injects it.
    """

    def messages(
        self, query: str, passages: list[RetrievalResult], history: Sequence[HistoryTurn]
    ) -> list[LLMMessage]:
        """Return the LLM messages to answer ``query`` from ``passages``.

        They instruct the model.

        ``history`` is rendered as non-citable context; passages remain the only citable
        source.

        Args:
            query: The question.
            passages: The retrieved passages.
            history: The conversation so far.

        Returns:
            The messages to send.

        """
        ...


@final
class ResponseGenerator(AnswerGenerator):
    """Generates a cited answer from a query and retrieved passages.

    An LLM and an injected prompt do the work.
    """

    def __init__(
        self, llm: LLMProvider, prompt: GenerationPrompt, config: LLMConfig | None = None
    ) -> None:
        """Bind the LLM provider, the generation prompt and the decoding config.

        The consumer supplies the prompt; the config is per call.

        ``config`` carries the max_tokens/temperature/top_p the composition root resolves
        from the consumer's config; ``None`` lets the provider apply its conservative
        defaults.

        Args:
            llm: The model.
            prompt: Builds the messages.
            config: Per-call settings; ``None`` uses the model's defaults.

        """
        self._llm = llm
        self._prompt = prompt
        self._config = config

    @override
    async def stream(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> AsyncIterator[str]:
        """Stream the generated answer token by token.

        History reaches the prompt as non-citable context.

        Args:
            query: The user's query.
            passages: The retrieved passages to ground the answer in.
            history: Earlier conversation turns, oldest first; ``None`` when there are
                none.

        Yields:
            Each token.

        """
        iterator = await self._llm.stream(
            self._prompt.messages(query, passages, history or ()), self._config
        )
        async for delta in iterator:
            yield delta

    @override
    async def complete(
        self,
        query: str,
        passages: list[RetrievalResult],
        history: Sequence[HistoryTurn] | None = None,
    ) -> str:
        """Generate the full, non-streaming answer.

        History is non-citable context for the prompt.

        Args:
            query: The user's query.
            passages: The retrieved passages to ground the answer in.
            history: Earlier conversation turns, oldest first; ``None`` when there are
                none.

        Returns:
            The answer text.

        """
        response = await self._llm.complete(
            self._prompt.messages(query, passages, history or ()), self._config
        )
        return response.content
