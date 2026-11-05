"""Unit tests for the ``layout`` parser.

The block mapping runs on real ``DoclingDocument``s (``docling-core``). The parser runs its real
bounded, spawned child with an injected converter (full Docling, the ``layout`` extra, is not
installed in CI; a real Docling run is recorded in the plan's deviations).
"""

from __future__ import annotations

from pathlib import Path
from typing import TYPE_CHECKING

import pytest
from docling_core.types.doc.base import BoundingBox, Size
from docling_core.types.doc.common.reference import ProvenanceItem
from docling_core.types.doc.document import DoclingDocument
from docling_core.types.doc.items.table.table_data import TableCell, TableData
from docling_core.types.doc.labels import DocItemLabel

from techai_webutils.clients.parsing import ParserConfig, ParserKind, parser_from_config
from techai_webutils.clients.parsing.isolated import IsolationMemoryError, run_isolated
from techai_webutils.clients.parsing.isolated.isolated import MEMORY_EXCEEDED
from techai_webutils.clients.parsing.layout import (
    LayoutOptions,
    LayoutParser,
    parsed_from_docling,
)
from techai_webutils.core.domain import BlockType, DocumentFormat
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Callable

_PDF = b"%PDF-1.7\n%probe\n"


def _prov(page: int) -> ProvenanceItem:
    """Return a provenance on ``page``."""
    return ProvenanceItem(
        page_no=page, bbox=BoundingBox(l=0, t=0, r=1, b=1), charspan=(0, 1)
    )


def _cell(row: int, col: int, text: str, *, header: bool = False) -> TableCell:
    """Return a one-by-one table cell."""
    return TableCell(
        text=text,
        start_row_offset_idx=row,
        end_row_offset_idx=row + 1,
        start_col_offset_idx=col,
        end_col_offset_idx=col + 1,
        column_header=header,
    )


def _report(note: str = "") -> DoclingDocument:
    """Build a two-page report: title, heading, text, list, code, captioned table, furniture."""
    doc = DoclingDocument(name="report")
    for page in (1, 2):
        doc.add_page(page_no=page, size=Size(width=100, height=100))
    doc.add_text(label=DocItemLabel.PAGE_HEADER, text="ACME confidential", prov=_prov(1))
    doc.add_title(text="Annual report", prov=_prov(1))
    doc.add_heading(text="Results", level=1, prov=_prov(1))
    doc.add_text(label=DocItemLabel.TEXT, text=note or "Revenue grew.", prov=_prov(1))
    bullets = doc.add_list_group()
    doc.add_list_item(text="Costs fell.", prov=_prov(1), parent=bullets)
    doc.add_code(text="total = sum(costs)", prov=_prov(1))
    doc.add_heading(text="Costs", level=1, prov=_prov(2))
    caption = doc.add_text(
        label=DocItemLabel.CAPTION, text="Table 1: costs", prov=_prov(2)
    )
    data = TableData(
        num_rows=2,
        num_cols=2,
        table_cells=[
            _cell(0, 0, "Quarter", header=True),
            _cell(0, 1, "Cost", header=True),
            _cell(1, 0, "Q1"),
            _cell(1, 1, "10"),
        ],
    )
    doc.add_table(data=data, caption=caption, prov=_prov(2))
    return doc


def convert_report(path: str, options: LayoutOptions) -> DoclingDocument:
    """Stand in for Docling in the child: a report noting the page range and the file's size."""
    size = Path(path).stat().st_size
    return _report(f"Pages {options.first_page}-{options.last_page} of {size} bytes.")


def convert_out_of_memory(path: str, options: LayoutOptions) -> DoclingDocument:
    """Stand in for a conversion that runs out of memory in the child."""
    msg = f"out of memory converting {path} ({options.first_page})"
    raise MemoryError(msg)


def _allocate_too_much() -> None:
    """Raise ``MemoryError`` as an over-cap allocation would."""
    msg = "over the address-space cap"
    raise MemoryError(msg)


def _layout(convert: Callable[[str, LayoutOptions], DoclingDocument]) -> LayoutParser:
    """Build the layout parser through the factory with an injected converter."""
    parser = parser_from_config(
        ParserConfig(kind=ParserKind.LAYOUT, timeout_seconds=60), convert=convert
    )
    assert isinstance(parser, LayoutParser)
    return parser


def test_docling_items_map_to_typed_blocks_in_reading_order() -> None:
    """Test that a Docling document becomes typed blocks with sections, tables and pages.

    **Why this test is important:**
      - The chunker keeps a table whole and files each chunk under its heading only if the
        blocks say what each item is and where it sits.

    **What it tests:**
      - furniture (page header) is dropped; title, heading, paragraph, list item, code and table
        come back in order, each with its page and section path
      - the table keeps its 2x2 cells (header flagged), markdown rendering and caption, and its
        caption is not repeated as its own block
      - ``pages`` holds each page's block text; the format is PDF
    """
    parsed = parsed_from_docling(_report())

    assert [(b.type, b.page, b.section_path) for b in parsed.blocks] == [
        (BlockType.TITLE, 1, ("Annual report",)),
        (BlockType.HEADING, 1, ("Annual report", "Results")),
        (BlockType.PARAGRAPH, 1, ("Annual report", "Results")),
        (BlockType.LIST_ITEM, 1, ("Annual report", "Results")),
        (BlockType.CODE, 1, ("Annual report", "Results")),
        (BlockType.HEADING, 2, ("Annual report", "Costs")),
        (BlockType.TABLE, 2, ("Annual report", "Costs")),
    ]
    table = parsed.blocks[-1]
    assert (table.rows, table.cols, table.caption) == (2, 2, "Table 1: costs")
    assert [(c.row, c.col, c.text, c.header) for c in table.cells][:2] == [
        (0, 0, "Quarter", True),
        (0, 1, "Cost", True),
    ]
    assert "| Q1" in table.text
    assert [b.order for b in parsed.blocks] == list(range(7))
    assert parsed.document_format is DocumentFormat.PDF
    assert [p.page_number for p in parsed.pages] == [1, 2]
    assert "ACME confidential" not in parsed.pages[0].text


@pytest.mark.asyncio
async def test_pdf_parses_in_the_bounded_child_and_other_formats_fall_back(
    tmp_path: Path,
) -> None:
    """Test that PDFs go through the converter in the child, page ranges included; HTML does not.

    **Why this test is important:**
      - Docling must run inside the memory and time bounds, a large PDF must parse in parts, and
        formats Docling is not configured for must keep today's parser.

    **What it tests:**
      - PDF bytes and a PDF path both come back with blocks (the child read the file and ran the
        whole page range); ``parse_pages`` hands the child its page range
      - an HTML document is parsed by the ``isolated`` parser (no blocks)
    """
    path = tmp_path / "report.pdf"
    path.write_bytes(_PDF)
    parser = _layout(convert_report)

    from_bytes = await parser.parse(_PDF, filename="report.pdf")
    from_path = await parser.parse_path(str(path))
    part = await parser.parse_pages(str(path), first_page=3, last_page=4)
    html = await parser.parse(b"<html><body><h1>Hi</h1></body></html>", filename="a.html")

    texts = [b.text for b in part.blocks]
    assert from_bytes.ok
    assert from_path.ok
    assert part.ok
    assert f"of {len(_PDF)} bytes." in from_path.blocks[2].text
    assert from_bytes.blocks[2].text.startswith("Pages 1-")
    assert "Pages 3-4 of 16 bytes." in texts
    assert html.ok
    assert html.blocks == ()
    assert "Hi" in html.markdown_content


@pytest.mark.asyncio
async def test_out_of_memory_parse_fails_with_the_coded_memory_error() -> None:
    """Test that a child that runs out of memory fails its document with ``memory_exceeded``.

    **Why this test is important:**
      - A document over the memory cap must fail alone, with a reason a consumer can count and
        route (a bigger lane), not a crash or an opaque traceback.

    **What it tests:**
      - ``run_isolated`` raises ``IsolationMemoryError`` for a task that raises ``MemoryError``
      - the layout parser returns a failed PDF document tagged ``MEMORY_EXCEEDED``, which keeps the
        ``parse_isolation_failed`` prefix
    """
    with pytest.raises(IsolationMemoryError):
        run_isolated(_allocate_too_much, timeout_seconds=30)

    parsed = await _layout(convert_out_of_memory).parse(_PDF, filename="big.pdf")

    assert parsed.error == MEMORY_EXCEEDED
    assert parsed.error.startswith("parse_isolation_failed")
    assert parsed.document_format is DocumentFormat.PDF


def test_factory_refuses_layout_without_docling() -> None:
    """Test that ``layout`` refuses to build where Docling is not installed and nothing is injected.

    **Why this test is important:**
      - Selecting ``layout`` in an image without the extra must stop the deployable at start-up,
        not fail every PDF with an import error.

    **What it tests:**
      - with no ``docling`` package (the CI and dev environment) and no ``convert``,
        ``parser_from_config`` raises ``AppValueError``; ``isolated`` stays the default kind
    """
    with pytest.raises(AppValueError):
        parser_from_config(ParserConfig(kind=ParserKind.LAYOUT))
    assert ParserConfig().kind is ParserKind.ISOLATED
