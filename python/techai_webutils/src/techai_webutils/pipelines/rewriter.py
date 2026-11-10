"""LLM-backed query rewriter (the RAG rewrite step).

A conversation follow-up becomes a standalone query.

Three-step RAG pattern (rewrite -> retrieve -> generate). On the first turn (no history)
the query is passed through unchanged; otherwise the LLM rewrites it into a self-contained
query. The **prompt is dependency-injected** (a ``RewritePrompt``) so this generic
pipeline carries no product-specific prompt text — the consuming app supplies the prompt
strategy at its composition root.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol, final, override

from techai_webutils.core.interfaces.retrieval import QueryRewriter

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMProvider
    from techai_webutils.core.interfaces.retrieval import HistoryTurn


class RewritePrompt(Protocol):
    """Builds the rewrite LLM messages from the query and role-tagged history.

    The consumer injects it.
    """

    def messages(self, query: str, history: Sequence[HistoryTurn]) -> list[LLMMessage]:
        """Return the LLM messages to rewrite ``query`` given ``history``.

        They instruct the model.

        Args:
            query: The question.
            history: The conversation so far.

        Returns:
            The messages to send.

        """
        ...


@final
class LlmQueryRewriter(QueryRewriter):
    """Rewrites follow-up questions into standalone queries.

    An LLM and an injected prompt strategy do the work.
    """

    def __init__(
        self, llm: LLMProvider, prompt: RewritePrompt, config: LLMConfig | None = None
    ) -> None:
        """Bind the LLM provider, the rewrite prompt and the decoding config.

        The consumer supplies the prompt; the config is per call.

        ``config`` carries the decoding dials from the consumer's config — a low
        temperature keeps the rewrite deterministic; ``None`` lets the provider apply its
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
    async def rewrite(
        self, query: str, history: Sequence[HistoryTurn] | None = None
    ) -> list[str]:
        """Return a standalone query; passthrough when there is no conversation history.

        Args:
            query: The user's query.
            history: Earlier conversation turns, oldest first; ``None`` when there are
                none.

        Returns:
            A standalone query.

        """
        if not history:
            return [query]
        response = await self._llm.complete(
            self._prompt.messages(query, history), self._config
        )
        rewritten = response.content.strip()
        return [rewritten or query]
