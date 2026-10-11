"""LLM-backed query rewriter: conversation follow-up -> standalone query (RAG rewrite step).

Three-step RAG pattern (rewrite -> retrieve -> generate). On the first turn (no history) the query is
passed through unchanged; otherwise the LLM rewrites it into a self-contained query. The **prompt is
dependency-injected** (a ``RewritePrompt``) so this generic pipeline carries no product-specific prompt
text — the consuming app supplies the prompt strategy at its composition root.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.retrieval import QueryRewriter

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage, LLMProvider
    from techai_webutils.core.interfaces.retrieval import HistoryTurn


class RewritePrompt(Protocol):
    """Builds the rewrite LLM messages from the query + role-tagged history (DI'd by the consumer)."""

    def messages(self, query: str, history: Sequence[HistoryTurn]) -> list[LLMMessage]:
        """Return the LLM messages instructing the model to rewrite ``query`` given ``history``."""
        ...


class LlmQueryRewriter(QueryRewriter):
    """Rewrites follow-up questions into standalone queries via an LLM + an injected prompt strategy."""

    def __init__(
        self, llm: LLMProvider, prompt: RewritePrompt, config: LLMConfig | None = None
    ) -> None:
        """Bind the LLM provider, the consumer-supplied rewrite prompt, and the per-call decoding config.

        ``config`` carries the decoding dials from the consumer's config — a low temperature keeps the
        rewrite deterministic; ``None`` lets the provider apply its defaults.
        """
        self._llm = llm
        self._prompt = prompt
        self._config = config

    async def rewrite(
        self, query: str, history: Sequence[HistoryTurn] | None = None
    ) -> list[str]:
        """Return a standalone query; passthrough when there is no conversation history."""
        if not history:
            return [query]
        response = await self._llm.complete(
            self._prompt.messages(query, history), self._config
        )
        rewritten = response.content.strip()
        return [rewritten or query]
