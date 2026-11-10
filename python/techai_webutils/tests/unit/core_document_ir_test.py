"""Unit tests for the document IR.

(typed blocks) and the structure fields on the parse/split DTOs.
"""

from __future__ import annotations

import json
from typing import cast

import pytest

from techai_webutils.core.domain import (
    BlockType,
    ChunkPayload,
    DocumentBlock,
    DocumentFormat,
    ParsedDocument,
    TableCell,
    Turn,
    blocks_from_json,
    blocks_to_json,
)
from techai_webutils.core.errors import AppValueError

_BLOCKS = (
    DocumentBlock(BlockType.TITLE, "Report", 0, 1, ("Report",)),
    DocumentBlock(BlockType.HEADING, "Costs", 1, 2, ("Report", "Costs"), level=1),
    DocumentBlock(
        BlockType.TABLE,
        "| Q | Cost |\n|---|---|\n| Q1 | 10 |",
        2,
        2,
        ("Report", "Costs"),
        cells=(TableCell(0, 0, "Q", header=True), TableCell(1, 1, "10", col_span=2)),
        rows=2,
        cols=2,
        caption="Table 1",
    ),
    DocumentBlock(
        BlockType.IMAGE, "Chart", 3, 3, ("Report",), caption="Chart", ref="img/1.png"
    ),
    DocumentBlock(
        BlockType.TURN,
        "Q. Why?",
        4,
        turn=Turn(
            "interviewer",
            0,
            40,
            47,
            speaker_id="s1",
            question_id="q2",
            follow_up=True,
            start_seconds=1.5,
            end_seconds=3.0,
        ),
    ),
)


def test_blocks_round_trip_through_json_at_this_and_the_previous_version() -> None:
    """Test that blocks survive the JSON form, and only versions N and N-1 are read.

    **Why this test is important:**
      - Blocks cross the parse child's scratch file and the stage checkpoints as JSON; a
        lossy round trip would drop a table's cells, and an unread version would strand
        checkpoints during a rollout.

    **What it tests:**
      - ``blocks_to_json`` -> ``json`` text -> ``blocks_from_json`` returns equal blocks
      - the previous ``format_version`` is read; a newer or missing one raises
        ``AppValueError``
    """
    payload = cast("dict[str, object]", json.loads(json.dumps(blocks_to_json(_BLOCKS))))

    assert blocks_from_json(payload) == _BLOCKS
    previous = {**payload, "format_version": cast("int", payload["format_version"]) - 1}
    assert blocks_from_json(previous) == _BLOCKS
    for version in (cast("int", payload["format_version"]) + 1, None):
        with pytest.raises(AppValueError):
            _ = blocks_from_json({**payload, "format_version": version})


def test_structure_fields_default_to_empty_so_existing_producers_are_unchanged() -> None:
    """Test that the new structure fields are optional on both DTOs.

    **Why this test is important:**
      - Every existing parser and splitter builds these DTOs without the new fields; a
        required field would break them all.

    **What it tests:**
      - ``ParsedDocument.blocks`` defaults to empty; ``ChunkPayload`` defaults to no
        section, no element type and no span; both keep their values when given
    """
    parsed = ParsedDocument(markdown_content="x", document_format=DocumentFormat.TXT)
    payload = ChunkPayload(chunk_index=0, text="x", page_start=None, page_end=None)
    located = ChunkPayload(0, "x", 1, 1, ("Report",), "table", (0, 1))

    assert parsed.blocks == ()
    assert (payload.section_path, payload.element_type, payload.span) == ((), "", None)
    assert (located.section_path, located.element_type, located.span) == (
        ("Report",),
        "table",
        (0, 1),
    )
    assert _BLOCKS[2].element_type == "table"


def test_turn_refuses_a_negative_index_or_an_empty_span() -> None:
    """Test that a turn with an impossible position is refused.

    **Why this test is important:**
      - Chunks attach turns by span; an inverted span would attach to nothing or
        everything.

    **What it tests:**
      - a negative index, a negative start and an empty span each raise ``AppValueError``
    """
    for index, start, end in ((-1, 0, 1), (0, -1, 1), (0, 5, 5)):
        with pytest.raises(AppValueError):
            _ = Turn("respondent", index, start, end)
