"""``LayoutParser``: PDFs through Docling, bounded.

Typed blocks, reading order, tables and OCR.

A PDF is converted by Docling inside the same bounded child process as the ``isolated``
parser (the address-space cap and the wall-clock timeout, both from the parser config),
and comes back as a ``ParsedDocument`` with ``blocks`` and Docling's markdown. Every other
format falls back to the ``isolated`` parser unchanged. ``parse_pages`` converts one page
range, so a consumer can parse a large PDF in parts and checkpoint each; ``page_count``
(also in the bounded child) says how many pages there are, so the parser is a
``PagedParser``. It is also a ``TextLayerParser``: ``text_layers`` reads each page's text
layer in the bounded child (pymupdf) and counts its characters and the unreadable ones,
and ``ocr_pages`` converts a page range with full-page OCR, so a consumer can OCR the
pages whose text layer is garbled.

Docling (the ``layout`` extra) is imported only in the child. Its model weights load from
``artifacts_path`` when set (a directory baked into the image, so a parse downloads
nothing); when empty Docling fetches them on first use. ``ocr`` turns on Docling's OCR for
pages and regions with no text layer. Each conversion (a whole PDF or one page range) is
one bounded child, so it costs ``cost()``: the address-space cap and one CPU. The child is
spawned, not forked: Docling's native libraries (torch) abort in a forked child on macOS,
and the forkserver does not preload Docling, so a fresh interpreter costs about the same.
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
from typing import TYPE_CHECKING, Protocol, Self, cast, final, override

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
    from collections.abc import Callable, Iterator
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
    """Converts the PDF at a path to a Docling document.

    ``convert_with_docling``, or a test's converter.

    A named type, so ``parser_from_config``'s signature stays fully known to a consumer
    that does not install the ``layout`` extra (and so cannot resolve
    ``DoclingDocument``).
    """

    def __call__(self, path: str, options: LayoutOptions, /) -> DoclingDocument:
        """Convert the PDF at ``path`` under ``options``.

        Args:
            path: The PDF file.
            options: The pages and OCR settings.

        Returns:
            Docling's document.

        """
        ...


class _InputFormats(Protocol):
    """Docling's input-format enum, as far as this parser reads it."""

    PDF: object
    """The PDF format."""


class _OcrOptions(Protocol):
    """The OCR settings this parser sets."""

    force_full_page_ocr: bool
    """Whether every page is OCRed."""


class _PdfOptions(Protocol):
    """Docling's PDF pipeline options, as far as this parser sets them."""

    ocr_options: _OcrOptions
    """The OCR settings."""


class _ConvertResult(Protocol):
    """A Docling conversion result."""

    document: DoclingDocument
    """The converted document."""


class _Converter(Protocol):
    """Docling's document converter."""

    def convert(self, path: str, *, page_range: tuple[int, int]) -> _ConvertResult:
        """Convert the file at ``path``.

        Args:
            path: The file.
            page_range: The first and last page, from 1.

        Returns:
            The conversion, carrying ``document``.

        """
        ...


class _PdfPage(Protocol):
    """A PyMuPDF page."""

    def get_text(self) -> str:
        """Return the page's text layer.

        Returns:
            The page's text layer.

        """
        ...


class _PdfDocument(Protocol):
    """An open PyMuPDF document."""

    page_count: int
    """How many pages it has."""

    def __enter__(self) -> Self:
        """Keep the document open for the block.

        Returns:
            The open document.

        """
        ...

    def __exit__(self, *exc: object) -> None:
        """Close the document.

        Args:
            *exc: The exception triple, if any.

        """
        ...

    def __iter__(self) -> Iterator[_PdfPage]:
        """Iterate the pages in order.

        Returns:
            An iterator over the pages.

        """
        ...


def _open_pdf(path: str) -> _PdfDocument:
    """Open the PDF at ``path`` (PyMuPDF loads only in the parse child).

    Args:
        path: The PDF file.

    Returns:
        The open document.

    """
    opener = cast("Callable[..., _PdfDocument]", importlib.import_module("pymupdf").open)
    return opener(path, filetype="pdf")


def convert_with_docling(path: str, options: LayoutOptions) -> DoclingDocument:
    """Convert the PDF at ``path`` with Docling.

    It runs in the parse child and imports Docling there.

    Args:
        path: The PDF file.
        options: The pages and OCR settings.

    Returns:
        The PDF at ``path`` with Docling.

    """
    converter_module = importlib.import_module("docling.document_converter")
    base_models = importlib.import_module("docling.datamodel.base_models")
    pipeline_options = importlib.import_module("docling.datamodel.pipeline_options")
    pdf_options = cast("Callable[..., _PdfOptions]", pipeline_options.PdfPipelineOptions)(
        do_ocr=options.ocr,
        do_table_structure=True,
        artifacts_path=options.artifacts_path or None,
    )
    if options.force_ocr:
        # The attribute every Docling release accepts (newer ones map it onto
        # OcrMode.FULL_PAGE).
        pdf_options.ocr_options.force_full_page_ocr = True
    format_option = cast("Callable[..., object]", converter_module.PdfFormatOption)
    pdf_format = cast("_InputFormats", base_models.InputFormat).PDF
    converter = cast("Callable[..., _Converter]", converter_module.DocumentConverter)(
        format_options={pdf_format: format_option(pipeline_options=pdf_options)}
    )
    result = converter.convert(path, page_range=(options.first_page, options.last_page))
    return result.document


def _write_scratch_pdf(content: bytes) -> str:
    """Write ``content`` to a new temporary ``.pdf`` file and return its path.

    Args:
        content: The PDF's bytes.

    Returns:
        The path of the scratch file.

    """
    fd, scratch = tempfile.mkstemp(suffix=".pdf")
    with os.fdopen(fd, "wb") as handle:
        _ = handle.write(content)
    return scratch


def _parse_layout(
    path: str,
    options: LayoutOptions,
    convert: LayoutConverter,
) -> ParsedDocument:
    """Child entry: convert the PDF and map it to a ``ParsedDocument``.

    Module-level, so it pickles.

    Args:
        path: The PDF file.
        options: The pages and OCR settings.
        convert: The layout converter.

    Returns:
        The parsed document.

    """
    return parsed_from_docling(convert(path, options))


def _count_pages(path: str) -> ParsedDocument:
    """Child entry: a document whose ``total_pages`` is the PDF's page count.

    0 when unreadable.

    Args:
        path: The PDF file.

    Returns:
        A document carrying only the page count.

    """
    try:
        with _open_pdf(path) as doc:
            count = doc.page_count
    except (RuntimeError, ValueError):
        count = 0
    return ParsedDocument("", DocumentFormat.PDF, total_pages=count)


def _page_texts(path: str) -> ParsedDocument:
    """Child entry: a document whose pages hold each page's text layer.

    None when unreadable.

    Args:
        path: The PDF file.

    Returns:
        A document carrying each page's text.

    """
    try:
        with _open_pdf(path) as doc:
            pages = [Page(n, page.get_text()) for n, page in enumerate(doc, start=1)]
    except (RuntimeError, ValueError):
        pages = []
    return ParsedDocument("", DocumentFormat.PDF, pages=pages, total_pages=len(pages))


_UNREADABLE = frozenset({"Cc", "Co", "Cs", "Cn"})
"""Unicode categories no reader renders: control, private use, surrogate and
unassigned."""


def _page_text(page: int, text: str) -> PageText:
    """Return ``text``'s non-whitespace characters and its unreadable ones.

    Args:
        page: The page number, from 1.
        text: The page's text layer.

    Returns:
        ``text``'s counts.

    """
    chars = [c for c in text if not c.isspace()]
    unreadable = sum(
        1 for c in chars if c == "\ufffd" or unicodedata.category(c) in _UNREADABLE
    )
    return PageText(page=page, chars=len(chars), unreadable=unreadable)


@dataclass(frozen=True, kw_only=True)
class LayoutParserOptions:
    """What ``LayoutParser`` takes, grouped into one value."""

    memory_bytes: int
    """The ``memory_bytes`` value of ``LayoutParser``."""

    timeout_seconds: float
    """The ``timeout_seconds`` value of ``LayoutParser``."""

    executor: Executor | None = None
    """The ``executor`` value of ``LayoutParser``."""

    artifacts_path: str = ""
    """The ``artifacts_path`` value of ``LayoutParser``."""

    ocr: bool = True
    """The ``ocr`` value of ``LayoutParser``."""

    convert: LayoutConverter = convert_with_docling
    """The ``convert`` value of ``LayoutParser``."""


@final
class LayoutParser(IsolatedParser):
    """Parses PDFs with Docling in a bounded child.

    Other formats go to the ``isolated`` parser.
    """

    _context = SPAWN_CONTEXT
    """Docling runs in a spawned child (see the module docs)."""

    def __init__(self, options: LayoutParserOptions) -> None:
        """Bound each parse like ``IsolatedParser``.

        ``convert`` runs in the child, so it must pickle.

        Args:
            options: The converter, limits and page settings.

        """
        super().__init__(
            memory_bytes=options.memory_bytes,
            timeout_seconds=options.timeout_seconds,
            executor=options.executor,
        )
        self._options = LayoutOptions(
            artifacts_path=options.artifacts_path, ocr=options.ocr
        )
        self._convert = options.convert

    @override
    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content``: a PDF through Docling, else ``isolated``.

        The PDF goes through a scratch file.

        Args:
            content: The file's raw bytes.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name, a type hint and kept on the result.

        Returns:
            The parsed document.

        """
        if detect_format(declared, filename, content) != DocumentFormat.PDF:
            return await super().parse(content, declared=declared, filename=filename)
        scratch = await asyncio.to_thread(_write_scratch_pdf, content)
        try:
            return await self._layout(scratch, self._options, declared, filename)
        finally:
            await asyncio.to_thread(Path(scratch).unlink, missing_ok=True)

    @override
    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse the file at ``path``: a PDF through Docling, else ``isolated``.

        Args:
            path: The file's local path.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name; empty uses the path's name.

        Returns:
            The file at ``path``: a PDF through Docling, else ``isolated``.

        """
        if detect_format(declared, filename or path, b"") != DocumentFormat.PDF:
            return await super().parse_path(path, declared=declared, filename=filename)
        return await self._layout(path, self._options, declared, filename)

    async def parse_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` of the PDF at ``path``.

        Pages are 1-based and inclusive.

        Args:
            path: The PDF's local path.
            first_page: The first page to parse, 1-based.
            last_page: The last page to parse, inclusive.
            filename: The original file name, kept on the result; empty when unknown.

        Returns:
            The parsed document.

        """
        options = LayoutOptions(
            self._options.artifacts_path, self._options.ocr, first_page, last_page
        )
        return await self._layout(path, options, "application/pdf", filename)

    async def ocr_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` at ``path`` with OCR.

        Every page is OCRed in full.

        Args:
            path: The PDF's local path.
            first_page: The first page to OCR, 1-based.
            last_page: The last page to OCR, inclusive.
            filename: The original file name, kept on the result; empty when unknown.

        Returns:
            The parsed document.

        """
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

        The file is untrusted, so pymupdf reads it in the same bounded child as a parse. A
        non-PDF, an unreadable file, or a child that fails returns no pages: the caller
        parses as usual.

        Args:
            path: The PDF's local path.

        Returns:
            One entry per page; empty if the text layer cannot be read.

        """
        if detect_format("", path, b"") != DocumentFormat.PDF:
            return ()
        try:
            read = await self._isolated(_page_texts, path)
        except (TimeoutError, IsolationError):
            return ()
        return tuple(_page_text(p.page_number, p.text) for p in read.pages)

    async def page_count(self, path: str) -> int:
        """Count the pages of the PDF at ``path`` in the bounded child.

        0 for a non-PDF or a bad file.

        The file is untrusted, so pymupdf opens it in the same bounded child as a parse,
        not in this process. A count of 0 tells the caller to parse the document whole.

        Args:
            path: The PDF's local path.

        Returns:
            The pages of the PDF at ``path`` in the bounded child.

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
        """Run the Docling conversion in the bounded child.

        A failure becomes a failed document.

        Args:
            path: The PDF file.
            options: The pages and OCR settings.
            declared: The declared MIME type or extension.
            filename: The document's file name.

        Returns:
            The parsed document.

        """
        try:
            return await self._isolated(_parse_layout, path, options, self._convert)
        except TimeoutError:
            return self._failed_path(declared, filename or path, "parse_timeout")
        except IsolationError as exc:
            return self._failed_path(declared, filename or path, isolation_error_tag(exc))
