"""``LexicalCitationScorer``: support as word overlap, on CPU, with no model.

The score is the share of the claim's content words (lower-cased word tokens that are not
common English function words) found in the passage. It catches a citation to an unrelated
passage; it cannot tell a paraphrase from a contradiction, which needs an entailment
(``nli``) scorer.
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.citation_scorer import CitationScorer

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.citation_scorer import ClaimPassage

_WORD = re.compile(r"\w+")
_FUNCTION_WORDS = frozenset([
    "a",
    "an",
    "and",
    "are",
    "as",
    "at",
    "be",
    "by",
    "for",
    "from",
    "has",
    "have",
    "in",
    "is",
    "it",
    "its",
    "of",
    "on",
    "or",
    "that",
    "the",
    "this",
    "to",
    "was",
    "were",
    "which",
    "with",
])
"""Words too common to show support; dropped from both sides."""


class LexicalCitationScorer(CitationScorer):
    """Scores support as the share of the claim's content words found in the passage."""

    @override
    async def score(self, pairs: Sequence[ClaimPassage]) -> list[float]:
        """Return each claim's content-word overlap with its passage (0.

        For an empty claim).
        """
        return [_overlap(pair.claim, pair.passage) for pair in pairs]


def _overlap(claim: str, passage: str) -> float:
    """Return the share of ``claim``'s content words that ``passage`` contains."""
    words = _content_words(claim)
    if not words:
        return 0.0
    return len(words & _content_words(passage)) / len(words)


def _content_words(text: str) -> set[str]:
    """Return ``text``'s lower-cased word tokens, without function words."""
    return {w for w in _WORD.findall(text.casefold()) if w not in _FUNCTION_WORDS}
