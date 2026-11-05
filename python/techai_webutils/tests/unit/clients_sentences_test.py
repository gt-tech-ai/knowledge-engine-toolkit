"""Unit tests for the sentence splitter (real spaCy blank pipelines; no model, no I/O)."""

from __future__ import annotations

from typing import cast
from unittest.mock import MagicMock

import pytest

from techai_webutils.clients.sentences import (
    SentenceSplitterConfig,
    SentenceSplitterKind,
    sentence_splitter_from_config,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.metrics import MetricsProvider
from techai_webutils.core.interfaces.sentence_splitter import TextSpan

_TEXT = (
    "Dr. Smith paid $3.50 at 5 p.m. on Jan. 3. He left the U.S. office early.  "
    "Revenue grew 12.5% (v2.1)! Was it enough?\nYes."
)


def _tiles(text: str, spans: list[TextSpan]) -> bool:
    """Return whether ``spans`` cover ``text`` end to end with no gap or overlap."""
    ends = [0, *(s.end for s in spans)]
    return ends[-1] == len(text) and all(
        span.start == end for span, end in zip(spans, ends, strict=False)
    )


def test_default_splitter_keeps_abbreviations_and_decimals_whole() -> None:
    """Test that the default splitter ends sentences only at sentence punctuation.

    **Why this test is important:**
      - A boundary after ``Dr.``, ``p.m.`` or inside ``3.50`` cuts a chunk mid-sentence, so the
        chunk's text loses its meaning and retrieval its match.

    **What it tests:**
      - the default config builds the ``sentencizer`` kind with English rules
      - the golden text splits into its five sentences; a sentence keeps one trailing space,
        and any further whitespace starts the next one
      - the spans tile the text: no character is lost between sentences
    """
    splitter = sentence_splitter_from_config(SentenceSplitterConfig())

    spans = splitter.split(_TEXT)

    assert SentenceSplitterConfig().kind is SentenceSplitterKind.SENTENCIZER
    assert [_TEXT[s.start : s.end] for s in spans] == [
        "Dr. Smith paid $3.50 at 5 p.m. on Jan. 3. ",
        "He left the U.S. office early. ",
        " Revenue grew 12.5% (v2.1)! ",
        "Was it enough?",
        "\nYes.",
    ]
    assert _tiles(_TEXT, spans)


def test_language_rules_and_fallback_to_multilanguage_rules() -> None:
    """Test that a block's language selects its rules, and a language without rules falls back.

    **Why this test is important:**
      - Mixed-language corpora split each block by its own language; a language spaCy cannot
        tokenize without an extra library (Japanese) must still split, visibly, not fail.

    **What it tests:**
      - German text splits with ``language="de"`` into its two sentences
      - ``ja`` (no SudachiPy) and ``zz`` (unknown) split with the ``xx`` rules, tiling the text,
        and ``sentence_splitter_fallback_total`` counts each language once, however many calls
    """
    metrics = MagicMock(spec=MetricsProvider)
    counter = metrics.counter.return_value
    splitter = sentence_splitter_from_config(
        SentenceSplitterConfig(), metrics=cast("MetricsProvider", metrics)
    )
    german = "Der Bericht ist fertig. Er kommt am Freitag."

    spans = splitter.split(german, language="de")
    for language in ("ja", "ja", "zz"):
        assert _tiles("One. Two.", splitter.split("One. Two.", language=language))

    assert [german[s.start : s.end] for s in spans] == [
        "Der Bericht ist fertig. ",
        "Er kommt am Freitag.",
    ]
    assert [c.kwargs for c in counter.inc.call_args_list] == [
        {"language": "ja"},
        {"language": "zz"},
    ]


def test_edge_texts_and_limits() -> None:
    """Test empty, blank and over-long texts.

    **Why this test is important:**
      - Blocks can be empty or whitespace after cleaning, and one huge block must fail loudly
        instead of exhausting memory.

    **What it tests:**
      - empty text has no spans; whitespace-only text is one span
      - text over ``max_chars`` raises ``AppValueError``
    """
    splitter = sentence_splitter_from_config(SentenceSplitterConfig(max_chars=20))

    assert splitter.split("") == []
    assert splitter.split("   ") == [TextSpan(0, 3)]
    with pytest.raises(AppValueError):
        splitter.split("x" * 21)


def test_factory_and_span_validation_refuse_bad_values() -> None:
    """Test that the factory refuses an unknown kind or bad limits, and a span its bad range.

    **Why this test is important:**
      - A misconfigured splitter must stop the deployable at start-up, not on the first block.

    **What it tests:**
      - an unknown kind, an empty default language and ``max_chars=0`` raise ``AppValueError``
      - ``TextSpan`` refuses a negative start and an empty or inverted range
    """
    for config in (
        SentenceSplitterConfig(kind=cast("SentenceSplitterKind", "blingfire")),
        SentenceSplitterConfig(default_language=""),
        SentenceSplitterConfig(max_chars=0),
    ):
        with pytest.raises(AppValueError):
            sentence_splitter_from_config(config)
    for start, end in ((-1, 2), (3, 3), (4, 1)):
        with pytest.raises(AppValueError):
            TextSpan(start, end)
