"""``LayoutParser``: PDFs through Docling (typed blocks, reading order, tables, OCR), bounded.

A PDF is converted by Docling inside the same bounded child process as the ``isolated`` parser
(the address-space cap and the wall-clock timeout, both from the parser config), and comes back
as a ``ParsedDocument`` with ``blocks`` and Docling's markdown. Every other format falls back to
the ``isolated`` parser unchanged. ``parse_pages`` converts one page range, so a consumer can
parse a large PDF in parts and checkpoint each; ``page_count`` (also in the bounded child) says how
many pages there are, so the parser is a ``PagedParser``. It is also a ``TextLayerParser``:
``text_layers`` reads each page's text layer in the bounded child (pymupdf) and counts its
characters and the unreadable ones, and ``ocr_pages`` converts a page range with full-page OCR,
so a consumer can OCR the pages whose text layer is garbled.

Docling (the ``layout`` extra) is imported only in the child. Its model weights load from
``artifacts_path`` when set (a directory baked into the image, so a parse downloads nothing);
when empty Docling fetches them on first use. ``ocr`` turns on Docling's OCR for pages and regions
with no text layer. Each conversion (a whole PDF or one page range) is one bounded child, so it
costs ``cost()``: the address-space cap and one CPU. The child is spawned, not forked: Docling's
native libraries (torch) abort in a forked child on macOS, and the forkserver does not preload
Docling, so a fresh interpreter costs about the same.
"""

from __future__ import annotations

import asyncio
import importlib
import os
import sys
import tempfile
import unicodedata
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Protocol, cast

from techai_webutils.clients.parsing.isolated import (
    SPAWN_CONTEXT,
    IsolatedParser,
    IsolationError,
    detect_format,
    isolation_error_tag,
)
from techai_webutils.clients.parsing.layout.mapping import parsed_from_docling
from techai_webutils.core.domain import DocumentFormat, Page, ParsedDocument
from techai_webutils.core.interfaces.parser import PageText

if TYPE_CHECKING:
    from concurrent.futures import Executor

    from docling_core.types.doc.document import DoclingDocument


@dataclass(frozen=True, slots=True)
class LayoutOptions:
    """What the child needs to convert a PDF (picklable)."""

    artifacts_path: str = ""
    """Directory holding Docling's model weights; empty lets Docling download them."""
    ocr: bool = True
    """Whether Docling runs OCR on pages and regions with no text layer."""
    first_page: int = 1
    """The first page to convert (1-based)."""
    last_page: int = sys.maxsize
    """The last page to convert (inclusive)."""
    force_ocr: bool = False
    """OCR every page in full, replacing its text layer (``ocr_pages``)."""


class LayoutConverter(Protocol):
    """Converts the PDF at a path to a Docling document (``convert_with_docling``, or a test's).

    A named type, so ``parser_from_config``'s signature stays fully known to a consumer that
    does not install the ``layout`` extra (and so cannot resolve ``DoclingDocument``).
    """

    def __call__(self, path: str, options: LayoutOptions, /) -> DoclingDocument:
        """Convert the PDF at ``path`` under ``options``."""
        ...


def convert_with_docling(path: str, options: LayoutOptions) -> DoclingDocument:
    """Convert the PDF at ``path`` with Docling (runs in the parse child; imports Docling there)."""
    converter_module = importlib.import_module("docling.document_converter")
    base_models = importlib.import_module("docling.datamodel.base_models")
    pipeline_options = importlib.import_module("docling.datamodel.pipeline_options")
    pdf_options = pipeline_options.PdfPipelineOptions(
        do_ocr=options.ocr,
        do_table_structure=True,
        artifacts_path=options.artifacts_path or None,
    )
    if options.force_ocr:
        # The attribute every Docling release accepts (newer ones map it onto OcrMode.FULL_PAGE).
        pdf_options.ocr_options.force_full_page_ocr = True
    converter = converter_module.DocumentConverter(
        format_options={
            base_models.InputFormat.PDF: converter_module.PdfFormatOption(
                pipeline_options=pdf_options
            )
        }
    )
    result = converter.convert(path, page_range=(options.first_page, options.last_page))
    return cast("DoclingDocument", result.document)


def _write_scratch_pdf(content: bytes) -> str:
    """Write ``content`` to a new temporary ``.pdf`` file and return its path."""
    fd, scratch = tempfile.mkstemp(suffix=".pdf")
    with os.fdopen(fd, "wb") as handle:
        handle.write(content)
    return scratch


def _parse_layout(
    path: str,
    options: LayoutOptions,
    convert: LayoutConverter,
) -> ParsedDocument:
    """Child entry: convert the PDF and map it to a ``ParsedDocument`` (module-level: picklable)."""
    return parsed_from_docling(convert(path, options))


def _count_pages(path: str) -> ParsedDocument:
    """Child entry: a document whose ``total_pages`` is the PDF's page count (0 if unreadable)."""
    pymupdf = importlib.import_module("pymupdf")
    try:
        with pymupdf.open(path, filetype="pdf") as doc:
            count = cast("int", doc.page_count)
    except (RuntimeError, ValueError):
        count = 0
    return ParsedDocument("", DocumentFormat.PDF, total_pages=count)


def _page_texts(path: str) -> ParsedDocument:
    """Child entry: a document whose pages hold each page's text layer (none if unreadable)."""
    pymupdf = importlib.import_module("pymupdf")
    try:
        with pymupdf.open(path, filetype="pdf") as doc:
            pages = [
                Page(n, cast("str", page.get_text()))
                for n, page in enumerate(doc, start=1)
            ]
    except (RuntimeError, ValueError):
        pages = []
    return ParsedDocument("", DocumentFormat.PDF, pages=pages, total_pages=len(pages))


_UNREADABLE = frozenset({"Cc", "Co", "Cs", "Cn"})
"""Unicode categories no reader renders: control, private use, surrogate and unassigned."""


def _page_text(page: int, text: str) -> PageText:
    """Return ``text``'s counts: its non-whitespace characters and the unreadable ones among them."""
    chars = [c for c in text if not c.isspace()]
    unreadable = sum(
        1 for c in chars if c == "\ufffd" or unicodedata.category(c) in _UNREADABLE
    )
    return PageText(page=page, chars=len(chars), unreadable=unreadable)


class LayoutParser(IsolatedParser):
    """Parses PDFs with Docling in a bounded child; other formats with the ``isolated`` parser."""

    _context = SPAWN_CONTEXT
    """Docling runs in a spawned child (see the module docs)."""

    def __init__(
        self,
        *,
        memory_bytes: int,
        timeout_seconds: float,
        executor: Executor | None = None,
        artifacts_path: str = "",
        ocr: bool = True,
        convert: LayoutConverter = convert_with_docling,
    ) -> None:
        """Bound each parse like ``IsolatedParser``; ``convert`` runs in the child (picklable)."""
        super().__init__(
            memory_bytes=memory_bytes, timeout_seconds=timeout_seconds, executor=executor
        )
        self._options = LayoutOptions(artifacts_path=artifacts_path, ocr=ocr)
        self._convert = convert

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content``: a PDF through Docling (via a scratch file), else ``isolated``."""
        if detect_format(declared, filename, content) != DocumentFormat.PDF:
            return await super().parse(content, declared=declared, filename=filename)
        scratch = await asyncio.to_thread(_write_scratch_pdf, content)
        try:
            return await self._layout(scratch, self._options, declared, filename)
        finally:
            await asyncio.to_thread(Path(scratch).unlink, missing_ok=True)

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse the file at ``path``: a PDF through Docling, else ``isolated``."""
        if detect_format(declared, filename or path, b"") != DocumentFormat.PDF:
            return await super().parse_path(path, declared=declared, filename=filename)
        return await self._layout(path, self._options, declared, filename)

    async def parse_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` (1-based, inclusive) of the PDF at ``path``."""
        options = LayoutOptions(
            self._options.artifacts_path, self._options.ocr, first_page, last_page
        )
        return await self._layout(path, options, "application/pdf", filename)

    async def ocr_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` of the PDF at ``path`` with full-page OCR."""
        options = LayoutOptions(
            self._options.artifacts_path,
            ocr=True,
            first_page=first_page,
            last_page=last_page,
            force_ocr=True,
        )
        return await self._layout(path, options, "application/pdf", filename)

    async def text_layers(self, path: str) -> tuple[PageText, ...]:
        """Count each page's text layer of the PDF at ``path`` in the bounded child.

        The file is untrusted, so pymupdf reads it in the same bounded child as a parse. A non-PDF,
        an unreadable file, or a child that fails returns no pages: the caller parses as usual.
        """
        if detect_format("", path, b"") != DocumentFormat.PDF:
            return ()
        try:
            read = await self._isolated(_page_texts, path)
        except (TimeoutError, IsolationError):
            return ()
        return tuple(_page_text(p.page_number, p.text) for p in read.pages)

    async def page_count(self, path: str) -> int:
        """Count the pages of the PDF at ``path`` in the bounded child; 0 for a non-PDF or bad file.

        The file is untrusted, so pymupdf opens it in the same bounded child as a parse, not in
        this process. A count of 0 tells the caller to parse the document whole.
        """
        if detect_format("", path, b"") != DocumentFormat.PDF:
            return 0
        try:
            counted = await self._isolated(_count_pages, path)
        except (TimeoutError, IsolationError):
            return 0
        return counted.total_pages

    async def _layout(
        self, path: str, options: LayoutOptions, declared: str, filename: str
    ) -> ParsedDocument:
        """Run the Docling conversion in the bounded child; a failure becomes a failed document."""
        try:
            return await self._isolated(_parse_layout, path, options, self._convert)
        except TimeoutError:
            return self._failed_path(declared, filename or path, "parse_timeout")
        except IsolationError as exc:
            return self._failed_path(declared, filename or path, isolation_error_tag(exc))
