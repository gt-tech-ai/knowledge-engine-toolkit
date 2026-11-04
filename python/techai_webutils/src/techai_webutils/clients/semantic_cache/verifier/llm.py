"""``LlmCacheVerifier`` — asks a (small) model whether two questions ask the same thing."""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.llm import LLMMessage
from techai_webutils.core.interfaces.semantic_cache import CacheVerifier
from techai_webutils.foundation.logger.logger import get_logger

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.llm import LLMProvider

_DEFAULT_INSTRUCTION = (
    "You decide whether two questions ask for the same information, so that one answer "
    "serves both. Different entities, numbers, dates, periods or scopes mean different "
    "questions. Reply with exactly one word: yes or no."
)
"""The system instruction of the default prompt."""


@dataclass(frozen=True, slots=True)
class CacheVerifierPrompt:
    """The prompt strategy the model verifier sends; injected so a product can replace it."""

    instruction: str = _DEFAULT_INSTRUCTION
    """The system message."""

    def messages(self, cached_query: str, query: str) -> list[LLMMessage]:
        """Return the messages asking whether ``query`` asks what ``cached_query`` asked."""
        return [
            LLMMessage(role="system", content=self.instruction),
            LLMMessage(
                role="user",
                content=f"Question A: {cached_query}\nQuestion B: {query}\nSame question?",
            ),
        ]


class LlmCacheVerifier(CacheVerifier):
    """Confirms only on a reply whose first word is "yes"; any failure is a "no"."""

    def __init__(
        self, llm: LLMProvider, prompt: CacheVerifierPrompt | None = None
    ) -> None:
        """Bind the (decorated) model provider and the prompt strategy."""
        self._llm = llm
        self._prompt = prompt or CacheVerifierPrompt()
        self._logger = get_logger("semantic_cache_verifier")

    @override
    async def same_question(self, cached_query: str, query: str) -> bool:
        """Return whether the model answers yes; a raising call counts as no."""
        try:
            response = await self._llm.complete(
                self._prompt.messages(cached_query, query)
            )
        except Exception as exc:
            self._logger.warning(
                "semantic cache verifier call failed (treated as no)",
                extra={"error": str(exc)},
            )
            return False
        words = response.content.strip().split()
        return bool(words) and words[0].strip(".,!:;").lower() == "yes"


_INTERFACE_CHECK: type[CacheVerifier] = LlmCacheVerifier
"""Type-checker assertion that ``LlmCacheVerifier`` satisfies ``CacheVerifier``."""
