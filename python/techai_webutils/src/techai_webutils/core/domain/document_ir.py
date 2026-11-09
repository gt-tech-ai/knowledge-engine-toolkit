"""The structured document: typed blocks in reading order (the parse IR).

A layout-aware parser (the ``layout`` kind) fills ``ParsedDocument.blocks`` beside
``markdown_content``; other parsers leave it empty. The block types follow Docling's
document model (title, section header, text, list item, table, picture, code, caption,
formula) so a Docling document maps onto them one to one; a ``turn`` block is one
speaker's turn in an interview transcript, written by the transcript source. Each block
keeps its page, the headings it sits under and its position in reading order; a table
keeps its cells, so its rows and header survive.

``blocks_to_json`` / ``blocks_from_json`` give the JSON form (plain dicts and lists, so
orjson or ``json`` can serialize it). It carries ``FORMAT_VERSION``; a reader accepts its
own version and the one before (N/N-1), so a writer can move first during a rollout.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import cast

from techai_webutils.core.errors import AppValueError

FORMAT_VERSION = 1
"""The version ``blocks_to_json`` writes."""


class BlockType(StrEnum):
    """What a block holds (Docling's item labels, collapsed to what chunking needs)."""

    TITLE = "title"
    """The document title."""
    HEADING = "heading"
    """A section heading; ``level`` is its depth (1 is outermost)."""
    PARAGRAPH = "paragraph"
    """Body text."""
    LIST_ITEM = "list_item"
    """One item of a bulleted or numbered list."""
    TABLE = "table"
    """A table; ``cells`` holds its grid and ``text`` its markdown rendering."""
    IMAGE = "image"
    """A picture; ``ref`` names the image and ``caption`` describes it."""
    CODE = "code"
    """A code listing."""
    CAPTION = "caption"
    """A caption not attached to a table or picture."""
    FORMULA = "formula"
    """A formula, as text (LaTeX where the parser produced it)."""
    TURN = "turn"
    """One speaker's turn in a transcript; ``turn`` holds who spoke and where."""


@dataclass(frozen=True, slots=True)
class TableCell:
    """One cell of a table block."""

    row: int
    """The cell's first row (0-based)."""
    col: int
    """The cell's first column (0-based)."""
    text: str
    """The cell's text."""
    header: bool = False
    """Whether the cell is a column or row header."""
    row_span: int = 1
    """Rows the cell covers."""
    col_span: int = 1
    """Columns the cell covers."""


@dataclass(frozen=True, slots=True)
class Turn:
    """One speaker's turn in an interview transcript."""

    speaker_role: str
    """The speaker's role (``interviewer``, ``respondent``, or the source's own term)."""
    index: int
    """The turn's position in the transcript (0-based)."""
    start: int
    """Offset of the turn's first character in the document's text."""
    end: int
    """Offset one past the turn's last character."""
    speaker_id: str = ""
    """The speaker's id in the source (empty when unknown)."""
    question_id: str = ""
    """The interview-guide question the turn answers or asks (empty when unknown)."""
    follow_up: bool = False
    """Whether the turn is a follow-up to the previous question."""
    start_seconds: float | None = None
    """When the turn starts in the recording (``None`` when the source has no
    timestamps)."""
    end_seconds: float | None = None
    """When the turn ends in the recording."""

    def __post_init__(self) -> None:
        """Refuse a negative index or an empty or inverted span.

        Raises:
            AppValueError: ``index < 0``, ``start < 0`` or ``end <= start``
                (``INVALID_INPUT``).

        """
        if self.index < 0 or self.start < 0 or self.end <= self.start:
            msg = f"invalid turn {self.index} [{self.start}, {self.end})"
            raise AppValueError(msg)


@dataclass(frozen=True, slots=True)
class DocumentBlock:
    """One block of a parsed document."""

    type: BlockType
    """What the block holds."""
    text: str
    """The block's text (a table's markdown rendering; a picture's caption)."""
    order: int
    """The block's position in reading order (0-based, unique within the document)."""
    page: int | None = None
    """The 1-based page the block starts on (``None`` for unpaginated formats)."""
    section_path: tuple[str, ...] = ()
    """The headings the block sits under, outermost first (a heading includes itself)."""
    level: int = 0
    """A heading's depth (1 is outermost); 0 for other blocks."""
    cells: tuple[TableCell, ...] = ()
    """A table's cells; empty for other blocks."""
    rows: int = 0
    """A table's row count; 0 for other blocks."""
    cols: int = 0
    """A table's column count; 0 for other blocks."""
    caption: str = ""
    """A table's or picture's caption."""
    ref: str = ""
    """A picture's reference (its URI or file name); empty for other blocks."""
    turn: Turn | None = None
    """A ``turn`` block's speaker and position; ``None`` for other blocks."""

    @property
    def element_type(self) -> str:
        """The block's type as the ``element_type`` string chunks carry."""
        return str(self.type)


def blocks_to_json(blocks: tuple[DocumentBlock, ...]) -> dict[str, object]:
    """Return ``blocks`` as a JSON-ready dict tagged with ``FORMAT_VERSION``."""
    return {
        "format_version": FORMAT_VERSION,
        "blocks": [
            {
                "type": str(b.type),
                "text": b.text,
                "order": b.order,
                "page": b.page,
                "section_path": list(b.section_path),
                "level": b.level,
                "cells": [
                    [c.row, c.col, c.text, c.header, c.row_span, c.col_span]
                    for c in b.cells
                ],
                "rows": b.rows,
                "cols": b.cols,
                "caption": b.caption,
                "ref": b.ref,
                "turn": _turn_to_json(b.turn),
            }
            for b in blocks
        ],
    }


def blocks_from_json(payload: dict[str, object]) -> tuple[DocumentBlock, ...]:
    """Read blocks written by ``blocks_to_json`` at this version or the one before.

    Raises:
        AppValueError: A ``format_version`` other than N or N-1 (``INVALID_INPUT``).

    """
    version = payload.get("format_version")
    if version not in {FORMAT_VERSION, FORMAT_VERSION - 1}:
        msg = f"unsupported document IR format_version {version!r}"
        raise AppValueError(msg)
    rows = cast("list[dict[str, object]]", payload.get("blocks", []))
    return tuple(_block(row) for row in rows)


def _block(row: dict[str, object]) -> DocumentBlock:
    """Build one block from its JSON dict."""
    cells = cast("list[list[object]]", row.get("cells", []))
    return DocumentBlock(
        type=BlockType(cast("str", row["type"])),
        text=cast("str", row["text"]),
        order=cast("int", row["order"]),
        page=cast("int | None", row.get("page")),
        section_path=tuple(cast("list[str]", row.get("section_path", []))),
        level=cast("int", row.get("level", 0)),
        cells=tuple(
            TableCell(
                cast("int", c[0]),
                cast("int", c[1]),
                cast("str", c[2]),
                cast("bool", c[3]),
                cast("int", c[4]),
                cast("int", c[5]),
            )
            for c in cells
        ),
        rows=cast("int", row.get("rows", 0)),
        cols=cast("int", row.get("cols", 0)),
        caption=cast("str", row.get("caption", "")),
        ref=cast("str", row.get("ref", "")),
        turn=_turn_from_json(cast("dict[str, object] | None", row.get("turn"))),
    )


def _turn_to_json(turn: Turn | None) -> dict[str, object] | None:
    """Return ``turn`` as a JSON-ready dict (``None`` stays ``None``)."""
    if turn is None:
        return None
    return {
        "speaker_role": turn.speaker_role,
        "index": turn.index,
        "start": turn.start,
        "end": turn.end,
        "speaker_id": turn.speaker_id,
        "question_id": turn.question_id,
        "follow_up": turn.follow_up,
        "start_seconds": turn.start_seconds,
        "end_seconds": turn.end_seconds,
    }


def _turn_from_json(row: dict[str, object] | None) -> Turn | None:
    """Build a turn from its JSON dict (``None`` stays ``None``)."""
    if row is None:
        return None
    return Turn(
        speaker_role=cast("str", row["speaker_role"]),
        index=cast("int", row["index"]),
        start=cast("int", row["start"]),
        end=cast("int", row["end"]),
        speaker_id=cast("str", row.get("speaker_id", "")),
        question_id=cast("str", row.get("question_id", "")),
        follow_up=cast("bool", row.get("follow_up", False)),
        start_seconds=cast("float | None", row.get("start_seconds")),
        end_seconds=cast("float | None", row.get("end_seconds")),
    )
