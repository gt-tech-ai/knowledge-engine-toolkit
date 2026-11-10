"""Unit tests for the sentence chunker.

(real spaCy sentence splitter; a word-count tokenizer).
"""

from __future__ import annotations

import time
from itertools import pairwise
from typing import TYPE_CHECKING

import pytest
from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.clients.chunking import (
    ChunkerConfig,
    ChunkerKind,
    chunker_from_config,
)
from techai_webutils.clients.chunking.sentence import BlockKind, split_blocks
from techai_webutils.clients.sentences import (
    SentenceSplitterConfig,
    sentence_splitter_from_config,
)
from techai_webutils.core.domain import Turn
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import Entity
from techai_webutils.core.interfaces.chunker import StructuredChunk, StructuredChunker

if TYPE_CHECKING:
    from collections.abc import Callable

_EXPECTED_WORDS = 40
_EXPECTED_CHUNKS_COUNT = 2
_EXPECTED_ELAPSED = 10.0
_EXPECTED_VALUE = 512
_EXPECTED_VALUE_2 = 2

_SPLITTER = sentence_splitter_from_config(SentenceSplitterConfig())


def _words(text: str) -> int:
    """Count whitespace-separated words: a subadditive stand-in for a model tokenizer.

    Returns:
        The word count.

    """
    return len(text.split())


def _chunker(
    max_tokens: int, overlap_tokens: int = 0, tokenizer: Callable[[str], int] = _words
) -> StructuredChunker:
    """Build a sentence chunker through the factory.

    Returns:
        A sentence chunker through the factory.

    """
    chunker = chunker_from_config(
        ChunkerConfig(
            kind=ChunkerKind.SENTENCE,
            max_tokens=max_tokens,
            overlap_tokens=overlap_tokens,
        ),
        splitter=_SPLITTER,
        tokenizer=tokenizer,
    )
    assert isinstance(chunker, StructuredChunker)
    return chunker


def _assert_tiles(text: str, chunks: list[StructuredChunk]) -> None:
    """Assert the chunk spans, with overlaps removed, reproduce ``text`` exactly."""
    assert chunks[0].start == 0
    assert chunks[-1].end == len(text)
    rebuilt = text[chunks[0].start : chunks[0].end]
    for previous, chunk in pairwise(chunks):
        assert previous.start < chunk.start <= previous.end < chunk.end
        rebuilt += text[previous.end : chunk.end]
    assert rebuilt == text


_DOC = """# Annual report

Revenue grew 12.5% in Q3. Dr. Lee led the work.

## Costs

Table 2: Costs by quarter
| Quarter | Cost |
|---|---|
| Q1 | 10 |
| Q2 | 12 |

```python
total = sum(costs)
print(total)
```


"""


def test_blocks_are_typed_sectioned_and_tile_the_text() -> None:
    """Test that a markdown text splits into typed blocks under their headings.

    **Why this test is important:**
      - Chunk metadata (section path, element type, caption) comes from these blocks; a
        table or code fence read as prose would be cut mid-row or mid-statement.

    **What it tests:**
      - the golden document yields heading, paragraph, heading, paragraph, table and code
        blocks, with section paths from the heading levels
      - the table's caption is the one-line paragraph before it; its header ends after the
        separator row; the blocks tile the text (trailing blank lines included)
    """
    blocks = split_blocks(_DOC)

    assert [(b.kind, b.section_path) for b in blocks] == [
        (BlockKind.HEADING, ("Annual report",)),
        (BlockKind.PARAGRAPH, ("Annual report",)),
        (BlockKind.HEADING, ("Annual report", "Costs")),
        (BlockKind.PARAGRAPH, ("Annual report", "Costs")),
        (BlockKind.TABLE, ("Annual report", "Costs")),
        (BlockKind.CODE, ("Annual report", "Costs")),
    ]
    table = blocks[4]
    assert table.caption == "Table 2: Costs by quarter"
    assert _DOC[table.start : table.header_end] == "| Quarter | Cost |\n|---|---|\n"
    assert "".join(_DOC[b.start : b.end] for b in blocks) == _DOC
    assert split_blocks("") == []
    assert [b.kind for b in split_blocks("\n\n")] == [BlockKind.PARAGRAPH]


def test_chunks_carry_section_type_caption_and_contained_entities() -> None:
    """Test that each chunk carries its section path, element type, caption and entities.

    **Why this test is important:**
      - Retrieval filters and cites by section and element type, and enrich's entities
        must follow their text into the chunk that holds it.

    **What it tests:**
      - the golden document chunks into a paragraph run (with its heading), a heading-led
        caption paragraph, the whole table and the whole code block, each with its section
      - an entity inside the first chunk is attached to it; one spanning two chunks to
        neither
    """
    lee = _DOC.index("Dr. Lee")
    entities = [
        Entity("PERSON", lee, lee + 7, "Dr. Lee"),
        Entity("X", lee, _DOC.index("Q1"), _DOC[lee : _DOC.index("Q1")]),
    ]

    chunks = _chunker(64).chunk_spans(_DOC, entities=entities)

    assert [(c.element_type, c.section_path) for c in chunks] == [
        ("paragraph", ("Annual report",)),
        ("paragraph", ("Annual report", "Costs")),
        ("table", ("Annual report", "Costs")),
        ("code", ("Annual report", "Costs")),
    ]
    assert chunks[0].entities == (entities[0],)
    assert all(e is not entities[1] for c in chunks for e in c.entities)
    assert chunks[2].caption == "Table 2: Costs by quarter"
    assert chunks[3].text.startswith("```python\ntotal = sum(costs)\nprint(total)\n```")
    _assert_tiles(_DOC, chunks)


def test_oversized_table_splits_between_rows_and_repeats_its_header() -> None:
    """Test that a table too big for one chunk splits by rows.

    Each piece led by the header.

    **Why this test is important:**
      - A table row read without its header is a list of numbers with no meaning; a row
        cut in two is worse.

    **What it tests:**
      - 60 rows at ``max_tokens=40`` give several table chunks, none over the budget
      - every piece starts with the header rows and holds only whole rows
    """
    header = "| Region | Units | Price |\n|---|---|---|\n"
    rows = "".join(f"| r{i} | {i} | {i * 3} |\n" for i in range(60))
    text = header + rows

    chunks = _chunker(40).chunk_spans(text)

    assert len(chunks) > 1
    for chunk in chunks:
        assert chunk.text.startswith(header)
        assert _words(chunk.text) <= _EXPECTED_WORDS
        assert all(
            line.startswith("| r") for line in chunk.text[len(header) :].splitlines()
        )
    _assert_tiles(text, chunks)


def test_overlap_repeats_the_previous_chunks_last_sentences() -> None:
    """Test that ``overlap_tokens`` repeats whole trailing sentences, never partial ones.

    **Why this test is important:**
      - Overlap keeps an answer that straddles a boundary retrievable from either chunk; a
        partial sentence would add noise instead.

    **What it tests:**
      - with ``overlap_tokens=5`` each later chunk starts at a sentence the previous chunk
        ended with; the spans still reproduce the text once overlaps are removed
    """
    text = " ".join(f"Sentence number {i} is here." for i in range(20))

    chunks = _chunker(20, overlap_tokens=5).chunk_spans(text)

    assert len(chunks) > _EXPECTED_CHUNKS_COUNT
    for previous, chunk in pairwise(chunks):
        assert chunk.start < previous.end
        assert text[chunk.start :].startswith("Sentence number")
    _assert_tiles(text, chunks)


def test_five_megabytes_of_unbroken_text_chunks_in_linear_time() -> None:
    """Test that a huge text with no whitespace or punctuation chunks quickly.

    And within budget.

    **Why this test is important:**
      - A scanned or binary-ish document can be one 5 MB "word"; a quadratic cut would
        stall the chunk stage for that document.

    **What it tests:**
      - 5,000,000 characters with a 4-chars-per-token tokenizer chunk in under 10 s, every
        chunk within ``max_tokens``, and the spans tile the text
    """
    text = "x" * 5_000_000

    def per_four(chunk: str) -> int:
        """Count one token per four characters.

        Returns:
            One token per four characters.

        """
        return (len(chunk) + 3) // 4

    started = time.perf_counter()
    chunks = _chunker(512, tokenizer=per_four).chunk_spans(text)
    elapsed = time.perf_counter() - started

    assert elapsed < _EXPECTED_ELAPSED
    assert max(per_four(c.text) for c in chunks) <= _EXPECTED_VALUE
    _assert_tiles(text, chunks)


_WORD = st.text(alphabet="abcdefgh", min_size=1, max_size=6)
_SENTENCE = st.lists(_WORD, min_size=1, max_size=25).map(
    lambda w: " ".join(w).capitalize() + "."
)
_PARAGRAPH = st.lists(_SENTENCE, min_size=1, max_size=6).map(" ".join)
_TABLE = st.lists(st.tuples(_WORD, _WORD), min_size=1, max_size=30).map(
    lambda rows: "| a | b |\n|---|---|\n" + "".join(f"| {x} | {y} |\n" for x, y in rows)
)
_CODE = st.lists(_WORD, min_size=1, max_size=15).map(
    lambda lines: "```\n" + "\n".join(lines) + "\n```"
)
_HEADING = _WORD.map(lambda w: f"## {w}")
_DOCUMENT = st.lists(
    st.one_of(_PARAGRAPH, _TABLE, _CODE, _HEADING), min_size=1, max_size=10
).map("\n\n".join)


@settings(max_examples=60, deadline=None)
@given(_DOCUMENT, st.sampled_from([8, 20, 64]), st.sampled_from([0, 3]))
def test_any_document_chunks_within_budget_and_tiles(
    text: str, max_tokens: int, overlap_tokens: int
) -> None:
    """Test, over generated documents.

    That no chunk exceeds the budget and nothing is lost.

    **Why this test is important:**
      - An over-budget chunk is truncated by the embedding model (its tail silently
        unsearchable); a gap between chunks drops text from the index.

    **What it tests:**
      - for any mix of paragraphs, tables, code and headings, budgets 8/20/64 and overlap
        0/3: every chunk's tokens are within ``max_tokens``, the spans tile the text with
        overlaps removed, and a chunk's text is its source span (a later table piece adds
        only its header)
    """
    chunks = _chunker(max_tokens, overlap_tokens).chunk_spans(text)

    assert all(_words(c.text) <= max_tokens for c in chunks)
    assert all(c.text.endswith(text[c.start : c.end]) for c in chunks)
    _assert_tiles(text, chunks)


def test_factory_refuses_a_sentence_chunker_it_cannot_build() -> None:
    """Test that the ``sentence`` kind refuses missing collaborators and bad limits.

    **Why this test is important:**
      - Token sizes come from the embedding model's tokenizer; a chunker built without
        one, or with an overlap as big as a chunk, must fail at start-up.

    **What it tests:**
      - no splitter, no tokenizer, ``overlap_tokens >= max_tokens``, ``max_tokens=0`` and
        ``window_chars=0`` raise ``AppValueError``; ``fixed`` stays the default kind
    """
    sentence = ChunkerConfig(kind=ChunkerKind.SENTENCE)
    with pytest.raises(AppValueError):
        _ = chunker_from_config(sentence, tokenizer=_words)
    with pytest.raises(AppValueError):
        _ = chunker_from_config(sentence, splitter=_SPLITTER)
    for config in (
        ChunkerConfig(kind=ChunkerKind.SENTENCE, max_tokens=10, overlap_tokens=10),
        ChunkerConfig(kind=ChunkerKind.SENTENCE, max_tokens=0),
        ChunkerConfig(kind=ChunkerKind.SENTENCE, window_chars=0),
    ):
        with pytest.raises(AppValueError):
            _ = chunker_from_config(config, splitter=_SPLITTER, tokenizer=_words)
    assert ChunkerConfig().kind is ChunkerKind.FIXED
    assert _chunker(64).chunk("One. Two.") == ["One. Two."]


def test_chunks_carry_every_turn_their_span_overlaps() -> None:
    """Test that each chunk carries the transcript turns it overlaps.

    A long turn in several.

    **Why this test is important:**
      - Retrieval cites and filters interview chunks by speaker and question; a chunk
        missing its turn loses who said it.

    **What it tests:**
      - with turns given out of order, each chunk carries exactly the turns whose span
        overlaps it, in start order; a turn spanning two chunks is on both
      - without turns, chunks carry none
    """
    text = "Q. Where were you? A. At home all day. I read. I slept. I woke up late."
    answer = text.index("A.")
    turns = [
        Turn("respondent", 1, answer, len(text)),
        Turn("interviewer", 0, 0, answer),
    ]

    chunks = _chunker(6).chunk_spans(text, turns=turns)

    assert len(chunks) > _EXPECTED_CHUNKS_COUNT
    for chunk in chunks:
        expected = tuple(
            t
            for t in sorted(turns, key=lambda t: t.start)
            if t.start < chunk.end and chunk.start < t.end
        )
        assert chunk.turns == expected
    assert sum(turns[0] in c.turns for c in chunks) >= _EXPECTED_VALUE_2
    assert all(c.turns == () for c in _chunker(6).chunk_spans(text))
