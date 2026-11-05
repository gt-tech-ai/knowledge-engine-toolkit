"""The structured document: typed blocks in reading order (the parse IR).

A layout-aware parser (the ``layout`` kind) fills ``ParsedDocument.blocks`` beside
``markdown_content``; other parsers leave it empty. The block types follow Docling's document
model (title, section header, text, list item, table, picture, code, caption, formula) so a
Docling document maps onto them one to one. Each block keeps its page, the headings it sits under
and its position in reading order; a table keeps its cells, so its rows and header survive.

``blocks_to_json`` / ``blocks_from_json`` give the JSON form (plain dicts and lists, so orjson or
``json`` can serialize it). It carries ``FORMAT_VERSION``; a reader accepts its own version and
the one before (N/N-1), so a writer can move first during a rollout.
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
    )
