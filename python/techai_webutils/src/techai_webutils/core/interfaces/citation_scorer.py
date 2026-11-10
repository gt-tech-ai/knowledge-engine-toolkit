"""``CitationScorer``: how well a cited passage supports the answer's claim.

A verifying citation extractor pairs each sentence of the answer with each passage its
``[n]`` markers cite and asks a scorer for the support, in ``[0, 1]``.
``LexicalCitationScorer`` (``pipelines.citation_scorers``) measures word overlap on CPU;
an entailment (NLI) scorer is the consumer's model, injected. The score becomes the
citation's confidence, never the retrieval score.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Sequence


@dataclass(frozen=True, slots=True)
class ClaimPassage:
    """A claim from the answer and a passage it cites."""

    claim: str
    """The answer's sentence, without its citation markers."""
    passage: str
    """The cited passage's text."""


class CitationScorer(ABC):
    """Scores how well each passage supports its claim."""

    @abstractmethod
    async def score(self, pairs: Sequence[ClaimPassage]) -> list[float]:
        """Return one support score in ``[0, 1]`` per pair, in order.

        Args:
            pairs: Each claim with the passage it cites.

        Returns:
            One support score in ``[0, 1]`` per pair, in the order of ``pairs``.

        """
