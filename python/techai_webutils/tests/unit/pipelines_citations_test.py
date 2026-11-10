"""Tests for citation extraction: positional (``off``) and verified.

(``lexical``, ``nli``).
"""

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.core.errors import AppValueError, InternalError
from techai_webutils.core.interfaces.citation_scorer import CitationScorer, ClaimPassage
from techai_webutils.core.interfaces.retrieval import RetrievalResult
from techai_webutils.pipelines.citation_scorers import LexicalCitationScorer
from techai_webutils.pipelines.citations import (
    CitationVerify,
    PassageCitationExtractor,
    UnsupportedCitation,
    keep_markers,
)

_EXPECTED_PAGE_NUMBER = 4
_EXPECTED_CITATIONS_COUNT = 3
_EXPECTED_CONFIDENCE = 0.5


def _source(
    doc: str, page: int | None, score: float = 0.9, chunk: str = "chunk"
) -> RetrievalResult:
    """Build a RetrievalResult for the given document/page/score/chunk text.

    Args:
        doc: The document.
        page: The page.
        score: Its score.
        chunk: The chunk.

    Returns:
        A RetrievalResult for the given document/page/score/chunk text.

    """
    return RetrievalResult(
        document_id=doc,
        document_name=f"{doc} name",
        chunk_content=chunk,
        score=score,
        page_number=page,
        metadata={},
    )


class TestPassageCitationExtractor:
    """Tests for the passage citation extractor."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_maps_passage_to_citation() -> None:
        """Test that a source passage maps to a Citation carrying its page + relevance.

        **Why this test is important:**
          - Page-level provenance is the trust feature; a citation missing the page (or
            score) can't be verified by the user.

        **What it tests:**
          - A single source yields one Citation with the document id, page, and score.
        """
        citations = await PassageCitationExtractor().extract(
            "answer [1]", [_source("doc-1", 4, 0.92)]
        )
        assert len(citations) == 1
        assert citations[0].document_id == "doc-1"
        assert citations[0].page_number == _EXPECTED_PAGE_NUMBER
        assert citations[0].confidence == pytest.approx(0.92)

    @staticmethod
    @pytest.mark.asyncio
    async def test_emits_one_citation_per_passage_in_order() -> None:
        """Test that every passage yields its own citation.

        Positionally aligned with the prompt.

        **Why this test is important:**
          - The generation prompt numbers passages 1-based, so the answer's ``[n]`` marker
            resolves to the n-th citation. Collapsing entries silently breaks that
            contract two
            ways: markers past the collapse point resolve to the WRONG source (a
            provenance failure), and markers past the end resolve to nothing, rendering as
            dead plain text in the UI.

        **What it tests:**
          - Four passages of one document yield four citations in retrieval order, so
            ``[n]`` maps to ``sources[n - 1]`` — including passages sharing a document
            with no page number, which a (document, page) collapse would have discarded.
        """
        sources = [_source("doc-1", None, chunk=f"passage {n}") for n in range(1, 5)]
        citations = await PassageCitationExtractor().extract(
            "cites [1] [2] [3] [4]", sources
        )
        assert [c.chunk for c in citations] == [
            "passage 1",
            "passage 2",
            "passage 3",
            "passage 4",
        ]

    @staticmethod
    @pytest.mark.asyncio
    async def test_keeps_repeated_passages_distinct() -> None:
        """Test that identical passages are not collapsed into one citation.

        **Why this test is important:**
          - Collapsing is tempting for display tidiness, but the prompt already numbered
            both copies separately; removing one renumbers every later citation and
            misattributes the answer's markers. Alignment outranks tidiness, so duplicates
            must survive extraction.

        **What it tests:**
          - Two byte-identical sources plus a third yield three citations, not two.
        """
        sources = [_source("doc-1", 4), _source("doc-1", 4), _source("doc-2", None)]
        citations = await PassageCitationExtractor().extract("a", sources)
        assert len(citations) == _EXPECTED_CITATIONS_COUNT
        assert [c.document_id for c in citations] == ["doc-1", "doc-1", "doc-2"]

    @staticmethod
    @pytest.mark.asyncio
    async def test_copies_the_consumers_attribute_keys() -> None:
        """Citations carry the passage metadata keys the consumer asks for.

        As ``attributes``.

        **Why this test is important:**
          - What a citation shows (badges, links, icons) is the consumer's product
            decision; fixed citation fields force one product's UI on every consumer.

        **What it tests:**
          - With attribute_keys=("color", "owner"), a citation's attributes hold the
            present keys only.
          - With no attribute_keys, attributes stay empty.
        """
        source = RetrievalResult(
            document_id="d",
            document_name="d",
            chunk_content="c",
            score=0.9,
            page_number=None,
            metadata={"color": "blue", "size": "L"},
        )

        [cited] = await PassageCitationExtractor(
            attribute_keys=("color", "owner")
        ).extract("", [source])
        [plain] = await PassageCitationExtractor().extract("", [source])

        assert cited.attributes == {"color": "blue"}
        assert plain.attributes == {}


_SOURCES = [
    _source("doc-1", 1, 0.9, chunk="Revenue grew 3% in the third quarter of 2025."),
    _source("doc-2", 2, 0.8, chunk="The office cafeteria serves lunch daily."),
    _source("doc-3", 3, 0.7, chunk="Headcount rose to 410 employees."),
]
_ANSWER = "Revenue grew 3% in the third quarter [1, 2]. Headcount rose to 410.\n[3] [9]"


def _nli(scores: list[float]) -> MagicMock:
    """Return a mocked entailment scorer answering ``scores``.

    Args:
        scores: The NLI scores, in order.

    Returns:
        A mocked entailment scorer answering ``scores``.

    """
    scorer = MagicMock(spec=CitationScorer)
    scorer.score = AsyncMock(return_value=scores)
    return scorer


class TestVerifiedCitations:
    """Tests for the verifying modes of the citation extractor."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_off_equals_the_positional_output() -> None:
        """Test that ``verify=off`` (the default) is exactly today's positional output.

        **Why this test is important:**
          - Verification is a dial; a consumer that does not turn it on must see no change
            in what it renders or pays.

        **What it tests:**
          - an explicit ``off`` extractor returns what the default one returns: every
            passage, in order, retrieval score as confidence, ``marker`` and ``supported``
            unset
        """
        default = await PassageCitationExtractor().extract(_ANSWER, _SOURCES)
        off = await PassageCitationExtractor(verify=CitationVerify.OFF).extract(
            _ANSWER, _SOURCES
        )

        assert off == default
        assert [c.confidence for c in off] == [0.9, 0.8, 0.7]
        assert all(c.marker is None and c.supported is None for c in off)

    @staticmethod
    @pytest.mark.asyncio
    async def test_lexical_keeps_cited_passages_and_flags_the_unsupported() -> None:
        """Test that ``lexical`` returns only cited passages, scored by overlap.

        With no model call.

        **Why this test is important:**
          - A citation to a passage that does not say what the answer says is the trust
            failure verification exists to catch, and the cheap check must cost no model
            call.

        **What it tests:**
          - markers ``[1, 2]`` and ``[3]`` (a marker on its own line cites the sentence
            before it) give citations 1, 2, 3 with their markers; ``[9]`` is out of range
            and ignored
          - 1 and 3 overlap their claims fully (supported, confidence 1.0); 2 does not
            (``supported=False``, confidence under the threshold, not the retrieval score)
          - an injected scorer is never called by ``lexical``
        """
        nli = _nli([])
        extractor = PassageCitationExtractor(verify=CitationVerify.LEXICAL, scorer=nli)

        citations = await extractor.extract(_ANSWER, _SOURCES)

        assert [(c.marker, c.document_id, c.supported) for c in citations] == [
            (1, "doc-1", True),
            (2, "doc-2", False),
            (3, "doc-3", True),
        ]
        assert citations[0].confidence == pytest.approx(1.0)
        assert citations[1].confidence < _EXPECTED_CONFIDENCE
        cast("MagicMock", cast("AsyncMock", nli.score).assert_not_awaited)()
        assert await LexicalCitationScorer().score([ClaimPassage("", "x")]) == [0.0]

    @staticmethod
    @pytest.mark.asyncio
    async def test_nli_confidence_is_the_scorers_and_strip_drops_unsupported() -> None:
        """Test that ``nli`` takes the scorer's value as confidence.

        And ``strip`` drops the rest.

        **Why this test is important:**
          - A retrieval score says a passage is about the query, not that it supports the
            sentence citing it; showing it as citation confidence overstates the evidence.

        **What it tests:**
          - the scorer is called once with every (claim, passage) pair in marker order
          - each returned citation's confidence is the scorer's value; the unsupported one
            is dropped under ``strip`` and ``keep_markers`` removes its marker from the
            answer
        """
        nli = _nli([0.95, 0.1, 0.8])
        extractor = PassageCitationExtractor(
            verify=CitationVerify.NLI,
            scorer=nli,
            unsupported=UnsupportedCitation.STRIP,
        )

        citations = await extractor.extract(_ANSWER, _SOURCES)

        claim = "Revenue grew 3% in the third quarter."
        cast("MagicMock", cast("AsyncMock", nli.score).assert_awaited_once_with)([
            ClaimPassage(claim, _SOURCES[0].chunk_content),
            ClaimPassage(claim, _SOURCES[1].chunk_content),
            ClaimPassage("Headcount rose to 410.", _SOURCES[2].chunk_content),
        ])
        assert [(c.marker, c.confidence) for c in citations] == [(1, 0.95), (3, 0.8)]
        assert keep_markers(_ANSWER, [c.marker for c in citations if c.marker]) == (
            "Revenue grew 3% in the third quarter [1]. Headcount rose to 410.\n[3]"
        )

    @staticmethod
    @pytest.mark.asyncio
    async def test_refuses_bad_settings_and_a_short_scorer_reply() -> None:
        """Test that a misconfigured or misbehaving verifier fails loudly.

        **Why this test is important:**
          - ``nli`` with no model, or a scorer dropping a score, would silently attach the
            wrong confidence to every later citation.

        **What it tests:**
          - ``nli`` without a scorer, an unknown mode and a threshold outside [0, 1] raise
            ``AppValueError``; a scorer returning too few scores raises ``InternalError``
          - an answer with no markers returns no verified citations and calls no scorer
        """
        with pytest.raises(AppValueError):
            _ = PassageCitationExtractor(verify=CitationVerify.NLI)
        with pytest.raises(AppValueError):
            _ = PassageCitationExtractor(min_confidence=1.5)
        with pytest.raises(AppValueError):
            _ = PassageCitationExtractor(
                unsupported=cast("UnsupportedCitation", cast("object", "drop"))
            )
        short = PassageCitationExtractor(verify=CitationVerify.NLI, scorer=_nli([0.9]))
        with pytest.raises(InternalError):
            _ = await short.extract(_ANSWER, _SOURCES)
        quiet = _nli([])
        unmarked = PassageCitationExtractor(verify=CitationVerify.NLI, scorer=quiet)
        assert await unmarked.extract("No citations here.", _SOURCES) == []
        cast("MagicMock", cast("AsyncMock", quiet.score).assert_not_awaited)()
