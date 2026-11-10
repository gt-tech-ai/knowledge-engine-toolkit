"""Unit tests for the ``layout`` parser.

The block mapping runs on real ``DoclingDocument``s (``docling-core``). The parser runs
its real bounded, spawned child with an injected converter (full Docling, the ``layout``
extra, is not installed in CI; a real Docling run is recorded in the plan's deviations).
"""

from __future__ import annotations

import importlib
from pathlib import Path
from typing import TYPE_CHECKING, NoReturn, cast
from unittest.mock import patch

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
from techai_webutils.core.interfaces.parser import PagedParser, PageText, TextLayerParser

if TYPE_CHECKING:
    from collections.abc import Callable
    from unittest.mock import MagicMock

_EXPECTED_PAGE_COUNT = 3

_PDF = b"%PDF-1.7\n%probe\n"


def _prov(page: int) -> ProvenanceItem:
    """Return a provenance on ``page``.

    Args:
        page: The page number.

    Returns:
        A provenance on ``page``.

    """
    return ProvenanceItem(
        page_no=page, bbox=BoundingBox(l=0, t=0, r=1, b=1), charspan=(0, 1)
    )


def _cell(row: int, col: int, text: str, *, header: bool = False) -> TableCell:
    """Return a one-by-one table cell.

    Args:
        row: The row.
        col: The column.
        text: The cell's text.
        header: Whether the cell is a header.

    Returns:
        A one-by-one table cell.

    """
    return TableCell(
        text=text,
        start_row_offset_idx=row,
        end_row_offset_idx=row + 1,
        start_col_offset_idx=col,
        end_col_offset_idx=col + 1,
        column_header=header,
    )


def _report(note: str = "") -> DoclingDocument:
    """Build a two-page report: title, heading, text, list, code, captioned table.

    Furniture.

    Args:
        note: Text added to the report.

    Returns:
        A two-page report: title, heading, text, list, code, captioned table.

    """
    doc = DoclingDocument(name="report")
    for page in (1, 2):
        _ = doc.add_page(page_no=page, size=Size(width=100, height=100))
    _ = doc.add_text(
        label=DocItemLabel.PAGE_HEADER, text="ACME confidential", prov=_prov(1)
    )
    _ = doc.add_title(text="Annual report", prov=_prov(1))
    _ = doc.add_heading(text="Results", level=1, prov=_prov(1))
    _ = doc.add_text(label=DocItemLabel.TEXT, text=note or "Revenue grew.", prov=_prov(1))
    bullets = doc.add_list_group()
    _ = doc.add_list_item(text="Costs fell.", prov=_prov(1), parent=bullets)
    _ = doc.add_code(text="total = sum(costs)", prov=_prov(1))
    _ = doc.add_heading(text="Costs", level=1, prov=_prov(2))
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
    _ = doc.add_table(data=data, caption=caption, prov=_prov(2))
    return doc


def convert_report(path: str, options: LayoutOptions) -> DoclingDocument:
    """Stand in for Docling in the child.

    A report noting the page range and the file's size.

    Args:
        path: The file.
        options: The layout options.

    Returns:
        The report document.

    """
    size = Path(path).stat().st_size
    return _report(f"Pages {options.first_page}-{options.last_page} of {size} bytes.")


def convert_noting_ocr(path: str, options: LayoutOptions) -> DoclingDocument:
    """Stand in for Docling in the child.

    A report noting the page range and whether OCR was forced.

    Args:
        path: The file.
        options: The layout options.

    Returns:
        The report document.

    """
    del path
    return _report(
        f"Pages {options.first_page}-{options.last_page} force_ocr={options.force_ocr}."
    )


def convert_out_of_memory(path: str, options: LayoutOptions) -> NoReturn:
    """Stand in for a conversion that runs out of memory in the child.

    Args:
        path: The file.
        options: The layout options.

    Raises:
        MemoryError: Always, naming ``path`` and the first page.

    """
    msg = f"out of memory converting {path} ({options.first_page})"
    raise MemoryError(msg)


def _allocate_too_much() -> None:
    """Raise ``MemoryError`` as an over-cap allocation would.

    Raises:
        MemoryError: Over the address-space cap.

    """
    msg = "over the address-space cap"
    raise MemoryError(msg)


def _layout(convert: Callable[[str, LayoutOptions], DoclingDocument]) -> LayoutParser:
    """Build the layout parser through the factory with an injected converter.

    Args:
        convert: Converts a file.

    Returns:
        The layout parser through the factory with an injected converter.

    """
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
      - furniture (page header) is dropped; title, heading, paragraph, list item, code and
        table come back in order, each with its page and section path
      - the table keeps its 2x2 cells (header flagged), markdown rendering and caption,
        and its caption is not repeated as its own block
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
    """Test that PDFs go through the converter in the child, page ranges included.

    HTML does not.

    **Why this test is important:**
      - Docling must run inside the memory and time bounds, a large PDF must parse in
        parts, and formats Docling is not configured for must keep today's parser.

    **What it tests:**
      - PDF bytes and a PDF path both come back with blocks (the child read the file and
        ran the whole page range); ``parse_pages`` hands the child its page range
      - an HTML document is parsed by the ``isolated`` parser (no blocks)

    Args:
        tmp_path: A fresh directory for the test.

    """
    path = tmp_path / "report.pdf"
    _ = path.write_bytes(_PDF)
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
    """Test that a child that runs out of memory fails its document.

    With ``memory_exceeded``.

    **Why this test is important:**
      - A document over the memory cap must fail alone, with a reason a consumer can count
        and route (a bigger lane), not a crash or an opaque traceback.

    **What it tests:**
      - ``run_isolated`` raises ``IsolationMemoryError`` for a task that raises
        ``MemoryError``
      - the layout parser returns a failed PDF document tagged ``MEMORY_EXCEEDED``, which
        keeps the ``parse_isolation_failed`` prefix
    """
    with pytest.raises(IsolationMemoryError):
        run_isolated(_allocate_too_much, timeout_seconds=30)

    parsed = await _layout(convert_out_of_memory).parse(_PDF, filename="big.pdf")

    assert parsed.error == MEMORY_EXCEEDED
    assert parsed.error.startswith("parse_isolation_failed")
    assert parsed.document_format is DocumentFormat.PDF


def test_factory_refuses_layout_without_docling() -> None:
    """Test that ``layout`` refuses to build where Docling is not installed.

    And nothing is injected.

    **Why this test is important:**
      - Selecting ``layout`` in an image without the extra must stop the deployable at
        start-up, not fail every PDF with an import error.

    **What it tests:**
      - with ``docling`` not importable (``find_spec`` mocked to ``None``, so the result
        does not depend on whether the extra is installed here) and no ``convert``,
        ``parser_from_config`` raises ``AppValueError``; ``isolated`` stays the default
        kind
    """
    with (
        patch(
            "techai_webutils.clients.parsing.factory.importlib.util.find_spec",
            return_value=None,
        ),
        pytest.raises(AppValueError),
    ):
        _ = parser_from_config(ParserConfig(kind=ParserKind.LAYOUT))
    assert ParserConfig().kind is ParserKind.ISOLATED


def _write_pdf(path: Path, pages: int) -> None:
    """Write a blank PDF of ``pages`` pages to ``path`` with pymupdf.

    Args:
        path: The file.
        pages: How many pages.

    """
    pymupdf = importlib.import_module("pymupdf")
    doc = cast("MagicMock", cast("MagicMock", pymupdf.open)())
    for _ in range(pages):
        cast("MagicMock", doc.new_page)()
    cast("MagicMock", doc.save)(str(path))
    cast("MagicMock", doc.close)()


@pytest.mark.asyncio
async def test_page_count_reads_the_pdf_in_the_child_and_counts_zero_otherwise(
    tmp_path: Path,
) -> None:
    """Test that the layout parser is a ``PagedParser`` that counts a PDF's pages safely.

    **Why this test is important:**
      - The parse stage splits a large PDF into page ranges from this count; a wrong count
        drops or repeats pages, and an unreadable or non-PDF file must fall back to one
        whole parse rather than fail the document before it is parsed.

    **What it tests:**
      - ``LayoutParser`` satisfies ``PagedParser``; a 3-page PDF counts 3
      - a file that is not a readable PDF, and an HTML file, count 0

    Args:
        tmp_path: A fresh directory for the test.

    """
    pdf = tmp_path / "three.pdf"
    _write_pdf(pdf, 3)
    junk = tmp_path / "junk.pdf"
    _ = junk.write_bytes(_PDF)
    html = tmp_path / "a.html"
    _ = html.write_bytes(b"<html></html>")
    parser = _layout(convert_report)

    assert isinstance(parser, PagedParser)
    assert await parser.page_count(str(pdf)) == _EXPECTED_PAGE_COUNT
    assert await parser.page_count(str(junk)) == 0
    assert await parser.page_count(str(html)) == 0


def _write_text_layers(path: Path) -> None:
    """Write a 3-page PDF: readable text, no text layer.

    And a text layer of control bytes.

    Args:
        path: The file.

    """
    pymupdf = importlib.import_module("pymupdf")
    doc = cast("MagicMock", cast("MagicMock", pymupdf.open)())
    cast("MagicMock", cast("MagicMock", doc.new_page)().insert_text)(
        (72, 72), "Revenue grew in every quarter."
    )
    cast("MagicMock", doc.new_page)()
    garbled = cast("MagicMock", cast("MagicMock", doc.new_page)())
    cast("MagicMock", garbled.insert_text)((72, 72), "x", fontname="helv")
    # Six bytes the font's encoding has no character for, then "ab": what a broken font
    # encoding leaves in a text layer.
    stream = b"q BT 1 0 0 1 72 770 Tm /helv 11 Tf [<0102030405066162>]TJ ET Q"
    cast("MagicMock", doc.update_stream)(
        cast("MagicMock", garbled.get_contents)()[0], stream
    )
    cast("MagicMock", doc.save)(str(path))
    cast("MagicMock", doc.close)()


@pytest.mark.asyncio
async def test_text_layers_count_each_page_and_its_unreadable_characters(
    tmp_path: Path,
) -> None:
    """Test that the layout parser reads each page's text layer in the child.

    And counts it.

    **Why this test is important:**
      - A consumer routes a page to OCR from these counts: a scanned page (no text layer)
        or one whose text layer is unreadable would otherwise be indexed as nothing or as
        garbage.

    **What it tests:**
      - ``LayoutParser`` is a ``TextLayerParser``; a readable page counts its characters
        with none unreadable, an empty page counts 0, and a control-byte text layer counts
        6 of 8 unreadable (share 0.75)
      - an HTML file returns no pages

    Args:
        tmp_path: A fresh directory for the test.

    """
    pdf = tmp_path / "layers.pdf"
    _write_text_layers(pdf)
    html = tmp_path / "a.html"
    _ = html.write_bytes(b"<html></html>")
    parser = _layout(convert_report)

    layers = await parser.text_layers(str(pdf))

    assert isinstance(parser, TextLayerParser)
    assert layers == (
        PageText(page=1, chars=26, unreadable=0),
        PageText(page=2, chars=0, unreadable=0),
        PageText(page=3, chars=8, unreadable=6),
    )
    assert layers[2].unreadable_share == pytest.approx(0.75)
    assert layers[1].unreadable_share == pytest.approx(0.0)
    assert await parser.text_layers(str(html)) == ()


@pytest.mark.asyncio
async def test_ocr_pages_forces_full_page_ocr_on_its_range(tmp_path: Path) -> None:
    """Test that ``ocr_pages`` converts its page range with OCR forced.

    And a plain parse does not.

    **Why this test is important:**
      - Docling OCRs only pages with no text layer; a garbled text layer is kept unless
        OCR is forced, so the consumer's routing would change nothing.

    **What it tests:**
      - ``ocr_pages(2, 3)`` hands the child pages 2-3 with ``force_ocr`` set
      - ``parse_pages`` over the same range leaves it unset

    Args:
        tmp_path: A fresh directory for the test.

    """
    path = tmp_path / "scan.pdf"
    _ = path.write_bytes(_PDF)
    parser = _layout(convert_noting_ocr)

    ocr = await parser.ocr_pages(str(path), first_page=2, last_page=3)
    plain = await parser.parse_pages(str(path), first_page=2, last_page=3)

    assert "Pages 2-3 force_ocr=True." in [b.text for b in ocr.blocks]
    assert "Pages 2-3 force_ocr=False." in [b.text for b in plain.blocks]
