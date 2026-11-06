"""Citation extraction from retrieved passages, positional or verified.

With ``verify`` ``off`` (the default) the extractor builds a ``Citation`` for **every** retrieved
passage (not only those the answer's ``[n]`` markers reference), one-to-one and in retrieval order.
The generator numbers passages 1-based in the prompt, so the answer's inline ``[n]`` marker
resolves to this list's n-th entry, which is why all retrieved passages are surfaced as potential
citations and why entries are never collapsed.

With ``verify`` ``lexical`` or ``nli`` it parses the answer's markers (``[2]``, ``[1, 3]``) and
returns only the cited passages, each with its ``marker``. Each is scored against the sentences
that cite it by a ``CitationScorer``: ``lexical`` (word overlap, no model) or the injected ``nli``
scorer. The best score is the citation's ``confidence``; a citation under ``min_confidence`` is
flagged ``supported=False`` or dropped (``unsupported``). ``keep_markers`` removes the markers of
dropped citations from the answer.
"""

from __future__ import annotations

import dataclasses
import re
from enum import StrEnum
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError, InternalError
from techai_webutils.core.interfaces.citation_scorer import ClaimPassage
from techai_webutils.core.interfaces.retrieval import Citation, CitationExtractor
from techai_webutils.core.interfaces.sink import CHUNK_ID_KEY
from techai_webutils.pipelines.citation_scorers import LexicalCitationScorer

if TYPE_CHECKING:
    from collections.abc import Collection, Sequence

    from techai_webutils.core.interfaces.citation_scorer import CitationScorer
    from techai_webutils.core.interfaces.retrieval import RetrievalResult

_MARKER = re.compile(r"( ?)\[(\d+(?:\s*,\s*\d+)*)\]")
"""An inline citation marker, ``[2]`` or ``[1, 3]``, with the space before it."""
_SENTENCE_BREAK = re.compile(r"(?<=[.!?])\s+|\n+")
"""Where the answer's sentences end (the claims a marker cites)."""


class CitationVerify(StrEnum):
    """Whether, and how, the extractor checks that cited passages support the answer."""

    OFF = "off"
    """Every passage, positional, with the retrieval score as confidence (the default)."""
    LEXICAL = "lexical"
    """The cited passages only, scored by word overlap on CPU (no model call)."""
    NLI = "nli"
    """The cited passages only, scored by the injected entailment scorer."""


class UnsupportedCitation(StrEnum):
    """What a verifying extractor does with a citation under ``min_confidence``."""

    FLAG = "flag"
    """Keep it with ``supported=False`` (the default)."""
    STRIP = "strip"
    """Drop it; ``keep_markers`` then removes its marker from the answer."""


class PassageCitationExtractor(CitationExtractor):
    """Maps retrieved passages to citations: all of them, or the verified cited ones."""

    def __init__(
        self,
        attribute_keys: Sequence[str] = (),
        *,
        verify: CitationVerify = CitationVerify.OFF,
        scorer: CitationScorer | None = None,
        min_confidence: float = 0.5,
        unsupported: UnsupportedCitation = UnsupportedCitation.FLAG,
    ) -> None:
        """Configure the extractor.

        ``attribute_keys`` names the passage metadata copied onto each citation's ``attributes``
        (badges, links, icons); keys a passage lacks are omitted. ``scorer`` is the entailment
        scorer ``nli`` uses; ``lexical`` uses ``LexicalCitationScorer`` and never calls it.

        Raises:
            AppValueError: An unknown ``verify`` or ``unsupported``, ``nli`` without a
                ``scorer``, or a ``min_confidence`` outside ``[0, 1]`` (``INVALID_INPUT``).

        """
        if verify not in set(CitationVerify) or unsupported not in set(
            UnsupportedCitation
        ):
            msg = f"unknown citation verify {verify!r} or unsupported {unsupported!r}"
            raise AppValueError(msg)
        if verify == CitationVerify.NLI and scorer is None:
            msg = "citation verify 'nli' needs an injected entailment scorer"
            raise AppValueError(msg)
        if not 0.0 <= min_confidence <= 1.0:
            msg = f"citation min_confidence must be in [0, 1], got {min_confidence}"
            raise AppValueError(msg)
        self._attribute_keys = tuple(attribute_keys)
        self._scorer: CitationScorer | None = (
            LexicalCitationScorer() if verify == CitationVerify.LEXICAL else scorer
        )
        self._verify = verify
        self._min_confidence = min_confidence
        self._unsupported = unsupported

    @override
    async def extract(
        self, answer: str, sources: list[RetrievalResult]
    ) -> list[Citation]:
        """Return the citations: every passage in order (``off``), or the verified cited ones.

        With ``off`` the mapping is strictly positional: the n-th entry is the passage the
        answer's ``[n]`` marker cites, so distinct passages stay distinct citations (collapsing
        them would slide every later index onto the wrong passage). Each citation names its
        chunk (the passage's ``chunk_id`` metadata) and cites the whole chunk (``span``
        ``None``). Verifying, the result is in marker order, one citation per cited passage;
        a marker outside ``1..len(sources)`` is ignored.

        Raises:
            InternalError: The scorer returned a different number of scores than pairs.

        """
        citations = [self._citation(source) for source in sources]
        scorer = self._scorer
        if self._verify == CitationVerify.OFF or scorer is None:
            return citations
        claims = _claims(answer, len(sources))
        markers = sorted(claims)
        pairs = [
            ClaimPassage(claim, sources[marker - 1].chunk_content)
            for marker in markers
            for claim in claims[marker]
        ]
        scores = await scorer.score(pairs) if pairs else []
        if len(scores) != len(pairs):
            msg = f"citation scorer returned {len(scores)} scores for {len(pairs)} pairs"
            raise InternalError(msg)
        verified: list[Citation] = []
        position = 0
        for marker in markers:
            count = len(claims[marker])
            best = max(scores[position : position + count])
            position += count
            supported = best >= self._min_confidence
            if not supported and self._unsupported == UnsupportedCitation.STRIP:
                continue
            verified.append(
                dataclasses.replace(
                    citations[marker - 1],
                    confidence=best,
                    marker=marker,
                    supported=supported,
                )
            )
        return verified

    def _citation(self, source: RetrievalResult) -> Citation:
        """Return ``source`` as a citation with the retrieval score as its confidence."""
        return Citation(
            document_id=source.document_id,
            document_name=source.document_name,
            chunk=source.chunk_content,
            page_number=source.page_number,
            confidence=source.score,
            attributes={
                k: source.metadata[k]
                for k in self._attribute_keys
                if k in source.metadata
            },
            chunk_id=source.metadata.get(CHUNK_ID_KEY, ""),
        )


def _claims(answer: str, count: int) -> dict[int, list[str]]:
    """Map each marker in ``1..count`` to the distinct answer sentences that cite it.

    A marker in a sentence of its own (``... grew 3%. [1]``) cites the sentence before it.
    """
    claims: dict[int, list[str]] = {}
    previous = ""
    for sentence in _SENTENCE_BREAK.split(answer):
        text = _MARKER.sub("", sentence).strip()
        claim = text or previous
        for match in _MARKER.finditer(sentence):
            for number in match.group(2).split(","):
                marker = int(number)
                cited = claims.setdefault(marker, []) if 1 <= marker <= count else None
                if cited is not None and claim and claim not in cited:
                    cited.append(claim)
        previous = claim
    return {marker: cited for marker, cited in claims.items() if cited}


def keep_markers(answer: str, keep: Collection[int]) -> str:
    """Return ``answer`` with every citation marker number not in ``keep`` removed.

    ``[1, 3]`` with ``keep={1}`` becomes ``[1]``; a marker left empty is removed with the space
    before it. Pass the ``marker`` of each citation a ``strip`` extractor returned.
    """
    kept = set(keep)

    def rewrite(match: re.Match[str]) -> str:
        numbers = [n.strip() for n in match.group(2).split(",") if int(n) in kept]
        return f"{match.group(1)}[{', '.join(numbers)}]" if numbers else ""

    return _MARKER.sub(rewrite, answer)
