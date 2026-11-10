"""``parsed_from_docling``: a Docling document as a ``ParsedDocument`` with typed blocks.

Docling's items map one to one onto ``BlockType``: title, section header (with its level),
text, list item, code, formula, table (cells, size, markdown rendering, caption) and
picture (caption and image reference). Page headers and footers are dropped (furniture,
not content), and a caption that belongs to a table or picture travels with it instead of
becoming its own block. Headings and the title build each block's ``section_path``.
``pages`` holds each page's block text, so citations can still address a page.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from docling_core.types.doc.items.code import CodeItem
from docling_core.types.doc.items.node import DocItem
from docling_core.types.doc.items.picture.picture import PictureItem
from docling_core.types.doc.items.table.table import TableItem
from docling_core.types.doc.items.text import (
    FormulaItem,
    ListItem,
    SectionHeaderItem,
    TextItem,
    TitleItem,
)
from docling_core.types.doc.labels import DocItemLabel

from techai_webutils.core.domain import (
    BlockType,
    DocumentBlock,
    DocumentFormat,
    Page,
    ParsedDocument,
    TableCell,
)

if TYPE_CHECKING:
    from docling_core.types.doc.document import DoclingDocument

_FURNITURE = frozenset({DocItemLabel.PAGE_HEADER, DocItemLabel.PAGE_FOOTER})
"""Labels dropped from the blocks: running headers and footers repeat on every page."""


def parsed_from_docling(doc: DoclingDocument) -> ParsedDocument:
    """Return ``doc`` as a PDF ``ParsedDocument`` with markdown.

    Per-page text and blocks.

    Args:
        doc: Docling's document.

    Returns:
        ``doc`` as a PDF ``ParsedDocument`` with markdown.

    """
    blocks = _blocks(doc)
    markdown = doc.export_to_markdown()
    page_numbers = sorted({b.page for b in blocks if b.page is not None})
    pages = [
        Page(n, "\n\n".join(b.text for b in blocks if b.page == n and b.text))
        for n in page_numbers
    ]
    return ParsedDocument(
        markdown_content=markdown,
        document_format=DocumentFormat.PDF,
        total_pages=max(doc.num_pages(), len(pages)),
        word_count=len(markdown.split()),
        pages=pages,
        blocks=tuple(blocks),
    )


def _blocks(doc: DoclingDocument) -> list[DocumentBlock]:
    """Map ``doc``'s items, in reading order, onto blocks.

    Args:
        doc: Docling's document.

    Returns:
        The blocks in reading order.

    """
    attached = {
        ref.cref for item in [*doc.tables, *doc.pictures] for ref in item.captions
    }
    path: list[tuple[int, str]] = []
    blocks: list[DocumentBlock] = []
    for item, _depth in doc.iterate_items():
        if not isinstance(item, DocItem) or item.label in _FURNITURE:
            continue
        if item.self_ref in attached:
            continue
        block = _block(doc, item, path, len(blocks))
        if block is not None:
            blocks.append(block)
    return blocks


def _block(
    doc: DoclingDocument, item: DocItem, path: list[tuple[int, str]], order: int
) -> DocumentBlock | None:
    """Map one item; update ``path`` for a title or heading.

    ``None`` for an empty item.

    Args:
        doc: Docling's document.
        item: The item.
        path: The open headings, by level; updated in place.
        order: The item's reading-order position.

    Returns:
        The block, or ``None`` for an item with no block.

    """
    page = item.prov[0].page_no if item.prov else None
    if isinstance(item, TitleItem | SectionHeaderItem):
        level = item.level if isinstance(item, SectionHeaderItem) else 0
        while path and path[-1][0] >= level:
            _ = path.pop()
        path.append((level, item.text))
        kind = (
            BlockType.HEADING if isinstance(item, SectionHeaderItem) else BlockType.TITLE
        )
        return DocumentBlock(kind, item.text, order, page, _path(path), level=level)
    if isinstance(item, TableItem):
        return _table(doc, item, order, page, _path(path))
    if isinstance(item, PictureItem):
        caption = item.caption_text(doc)
        ref = str(item.image.uri) if item.image is not None else ""
        return DocumentBlock(
            BlockType.IMAGE, caption, order, page, _path(path), caption=caption, ref=ref
        )
    if not isinstance(item, TextItem | CodeItem | FormulaItem) or not item.text.strip():
        return None
    return DocumentBlock(_text_type(item), item.text, order, page, _path(path))


def _text_type(item: TextItem | CodeItem | FormulaItem) -> BlockType:
    """Return the block type of a text-bearing item.

    Args:
        item: The text item.

    Returns:
        The block type of a text-bearing item.

    """
    if isinstance(item, ListItem):
        return BlockType.LIST_ITEM
    if isinstance(item, CodeItem):
        return BlockType.CODE
    if isinstance(item, FormulaItem):
        return BlockType.FORMULA
    if item.label == DocItemLabel.CAPTION:
        return BlockType.CAPTION
    return BlockType.PARAGRAPH


def _table(
    doc: DoclingDocument,
    item: TableItem,
    order: int,
    page: int | None,
    section_path: tuple[str, ...],
) -> DocumentBlock:
    """Map a table: its cells, size, markdown rendering and caption.

    Args:
        doc: Docling's document.
        item: The table.
        order: Its reading-order position.
        page: Its page, if known.
        section_path: The headings it sits under.

    Returns:
        The table block.

    """
    data = item.data
    cells = tuple(
        TableCell(
            row=cell.start_row_offset_idx,
            col=cell.start_col_offset_idx,
            text=cell.text,
            header=cell.column_header or cell.row_header,
            row_span=cell.row_span,
            col_span=cell.col_span,
        )
        for cell in data.table_cells
    )
    return DocumentBlock(
        BlockType.TABLE,
        item.export_to_markdown(doc),
        order,
        page,
        section_path,
        cells=cells,
        rows=data.num_rows,
        cols=data.num_cols,
        caption=item.caption_text(doc),
    )


def _path(path: list[tuple[int, str]]) -> tuple[str, ...]:
    """Return the heading texts of ``path``, outermost first.

    Args:
        path: The open headings, by level.

    Returns:
        The heading texts of ``path``, outermost first.

    """
    return tuple(text for _, text in path)
