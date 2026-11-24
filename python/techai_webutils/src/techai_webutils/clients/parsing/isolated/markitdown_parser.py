"""MarkItDown-based document parser with pymupdf PDF page extraction.

Microsoft's ``markitdown`` is the single unified conversion engine (all formats ->
Markdown); ``pymupdf`` runs *alongside* it for PDFs only, extracting page-by-page text +
boundaries for citation provenance. The parser never raises for a bad document: a
conversion failure is captured as ``error`` on the returned ``ParsedDocument`` so a single
poison document fails alone. A document that resolves to PDF (from its declared type or
extension) but carries no PDF header fails as ``conversion_failed`` before conversion:
MarkItDown would otherwise read its bytes as plain text and index them as the PDF.
A PDF whose Markdown carries far fewer words than its pymupdf page text (MarkItDown's
pdfplumber path drops the inter-word spaces of tightly typeset text) is indexed from the
page text instead; see ``_prefer_page_text``. A PDF whose text layer carries Private Use
Area characters (ligatures, small caps and oldstyle figures some typesetters place there)
has them replaced by the text their embedded glyph names spell; see ``glyphs``.
"""

from __future__ import annotations

import importlib
import io
import re
from pathlib import Path
from typing import TYPE_CHECKING, BinaryIO, Protocol, cast

from techai_webutils.clients.parsing.isolated.detect import detect_format, has_pdf_header
from techai_webutils.clients.parsing.isolated.glyphs import glyph_table
from techai_webutils.core.domain import DocumentFormat, Page, ParsedDocument
from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.logger import get_logger

if TYPE_CHECKING:
    from collections.abc import Callable, Collection, Mapping, Sequence
    from contextlib import AbstractContextManager

    from markitdown import MarkItDown as _LazyMarkItDown
    from markitdown import StreamInfo as _LazyStreamInfo

logger = get_logger(__name__)

# _HEADER_BYTES is how much of a file's prefix is read for magic-byte format detection on
# the path (streaming) lane — enough for the format sniffers, far less than the whole
# file.
_HEADER_BYTES = 4096
"""Bytes of a file's prefix read for magic-byte format detection on the streaming lane."""

# _WORD_RE matches runs of non-whitespace. Counting matches avoids materializing a full
# token list: ``len(text.split())`` allocates millions of small ``str`` objects for a
# large document just to take their length. ``pymupdf`` and ``markitdown`` are imported
# lazily inside the parse methods (not at module top) so the PARENT process that imports
# this class for the port never pays their heavy import cost — only a subprocess that
# actually parses does.
_WORD_RE = re.compile(r"\S+")
"""Matches runs of non-whitespace; counting matches avoids materializing a full token
list."""

_UNREADABLE_PDF = "conversion_failed: not a readable PDF (no %PDF- header)"
"""The error for a document resolved to PDF (by its declared type or extension) whose
bytes carry no PDF header: converting it would index the bytes as plain text."""

# _MAX_SANITIZE_BYTES bounds the lenient-decode fallback on the streaming (large) lane: a
# file whose bytes are not valid UTF-8 even after the forced-charset pass is re-read +
# sanitized only when it fits this cap, so the memory-bounded large lane never buffers a
# multi-GB file to recover a rare mis-encoding. Above the cap the original
# UnicodeDecodeError surfaces (recorded as conversion_failed).
_MAX_SANITIZE_BYTES = 64 << 20  # 64 MiB
"""Cap (64 MiB) on the lenient-decode fallback so the large lane never buffers a multi-GB
file to recover."""


_MIN_MARKDOWN_WORD_SHARE = 0.8
"""Below this share of the page text's word count, a PDF's Markdown has lost its word
boundaries (or its text) and the page text replaces it."""


class _ConversionResult(Protocol):
    """The slice of MarkItDown's result the parser reads."""

    text_content: str
    """The Markdown text MarkItDown produced from the source document."""


class _Converter(Protocol):
    """The slice of the MarkItDown API the parser depends on (injectable for tests)."""

    def convert_stream(self, stream: BinaryIO, **kwargs: object) -> _ConversionResult:
        """Convert a binary stream to a result carrying ``text_content``.

        The stream is in memory or an open file.

        Args:
            stream: The document's bytes.
            **kwargs: MarkItDown's options.

        Returns:
            The conversion, carrying ``text_content``.

        """
        ...


class _PdfPixmap(Protocol):
    """The slice of a pymupdf ``Pixmap`` the parser reads."""

    def tobytes(self, output: str) -> bytes:
        """Encode the bitmap as ``output`` (``"png"``).

        Args:
            output: The image format.

        Returns:
            The encoded image.

        """
        ...


class _PdfPage(Protocol):
    """The slice of a pymupdf ``Page`` the parser reads."""

    @property
    def rect(self) -> Sequence[float]:
        """The page's bounds as (x0, y0, x1, y1), in points."""
        ...

    def get_text(self, option: str = "text") -> object:
        """Return the page's text (pymupdf types it as ``str | list | dict``).

        Args:
            option: ``"text"`` for plain text, ``"rawdict"`` for each character with
                its bounding box.

        Returns:
            The page's text in the ``option`` shape.

        """
        ...

    def get_pixmap(self, *, dpi: int, clip: Sequence[float] | None = None) -> _PdfPixmap:
        """Render the page, or the ``clip`` rectangle of it, to a bitmap at ``dpi``.

        Args:
            dpi: The render resolution.
            clip: The (x0, y0, x1, y1) area to render; the whole page when ``None``.

        Returns:
            The rendered bitmap.

        """
        ...


class _PdfDocument(Protocol):
    """The slice of a pymupdf ``Document`` the parser reads.

    pymupdf leaves these untyped.
    """

    @property
    def page_count(self) -> int:
        """Number of pages in the document."""
        ...

    def load_page(self, page_id: int) -> _PdfPage:
        """Load the zero-based page ``page_id``.

        Args:
            page_id: The zero-based page number.

        Returns:
            The page.

        """
        ...


def _open_pdf(*args: object, **kwargs: object) -> AbstractContextManager[object]:
    """Open a PDF through PyMuPDF, which loads only when a parse needs it.

    Args:
        *args: PyMuPDF's ``open`` arguments.
        **kwargs: PyMuPDF's ``open`` keyword arguments.

    Returns:
        The open document, as a context manager.

    """
    opener = cast(
        "Callable[..., AbstractContextManager[object]]",
        importlib.import_module("pymupdf").open,
    )
    return opener(*args, **kwargs)


def _page_texts(doc: object) -> tuple[list[Page], dict[int, str]]:
    """Extract per-page text from an open pymupdf document, numbering pages from 1.

    Iterates by index: pymupdf's ``Document`` has no typed ``__iter__``, and its
    ``page_count`` / ``load_page`` are untyped, so the document is read through the
    ``_PdfDocument`` view. The pages' Private Use Area characters are replaced by the
    text their glyph names spell (``glyph_table``); the table comes back too, so the
    caller can translate the document's Markdown the same way.

    Args:
        doc: The open PyMuPDF document.

    Returns:
        The extracted pages, and the glyph table applied to them (empty when the text
        holds no Private Use Area character).

    """
    pdf = cast("_PdfDocument", doc)
    texts = [str(pdf.load_page(index).get_text()) for index in range(pdf.page_count)]
    table = glyph_table(doc, "".join(texts))
    pages = [
        Page(page_number=index + 1, text=text.translate(table))
        for index, text in enumerate(texts)
    ]
    return pages, table


def _prefer_page_text(markdown: str, pages: list[Page]) -> str:
    """Return ``markdown``, or the joined page text when ``markdown`` lost its words.

    MarkItDown converts a PDF with pdfminer, unless one page looks like a form or a table:
    then it extracts the whole document with pdfplumber, whose default spacing tolerance
    drops the inter-word spaces of tightly typeset text (LaTeX output, many books), so
    whole sentences index as one token and match no query. pymupdf's page text keeps the
    spaces, and the page-range path (``parse_pages``) already indexes it. When the
    Markdown's word count is under ``_MIN_MARKDOWN_WORD_SHARE`` of the page text's, the
    page text, one block per page, is the document's Markdown.

    Args:
        markdown: MarkItDown's Markdown for the PDF.
        pages: The PDF's pymupdf pages; empty when extraction failed.

    Returns:
        ``markdown``, or the joined page text when it holds far more words.

    """
    page_words = sum(sum(1 for _ in _WORD_RE.finditer(p.text)) for p in pages)
    markdown_words = sum(1 for _ in _WORD_RE.finditer(markdown))
    if markdown_words >= _MIN_MARKDOWN_WORD_SHARE * page_words:
        return markdown
    logger.warning(
        "pdf markdown lost words; using page text",
        markdown_words=markdown_words,
        page_words=page_words,
    )
    return "\n\n".join(page.text.strip() for page in pages)


class MarkItDownParser:
    """Parse document bytes into Markdown + (for PDFs) per-page text.

    The ``converter`` is injectable so the error path can be exercised deterministically;
    it defaults to a real ``MarkItDown`` instance.
    """

    def __init__(self, converter: _Converter | None = None) -> None:
        """Bind the MarkItDown converter (defaults to a fresh ``MarkItDown``).

        Args:
            converter: The converter; ``None`` builds a fresh ``MarkItDown``.

        """
        if converter is not None:
            self._converter: _Converter = converter
            return
        # Lazy import: the default converter is the only place markitdown is needed, and
        # it is constructed in the parse subprocess — the parent importing this class must
        # not pay the import. MarkItDown's convert_stream keyword params
        # (stream_info/file_extension) are narrower than the Protocol's **kwargs, so it is
        # not *structurally* assignable — cast the concrete default.
        mark_it_down_cls = cast(
            "type[_LazyMarkItDown]", importlib.import_module("markitdown").MarkItDown
        )

        self._converter = cast("_Converter", cast("object", mark_it_down_cls()))

    def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content`` into a ``ParsedDocument`` (never raises for a bad document).

        Args:
            content: The file's raw bytes.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name, a type hint and kept on the result.

        Returns:
            The parsed document.

        """
        fmt = detect_format(declared, filename, content)
        if fmt is DocumentFormat.UNKNOWN:
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error="unsupported_format",
            )
        if fmt is DocumentFormat.PDF and not has_pdf_header(content):
            return ParsedDocument(
                markdown_content="", document_format=fmt, error=_UNREADABLE_PDF
            )

        try:
            markdown = self._to_markdown(content, fmt)
        except Exception as exc:  # isolate one document's failure as error metadata
            logger.exception("parse failed")
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error=f"conversion_failed: {exc}",
            )

        pages, table = ([], {})
        if fmt is DocumentFormat.PDF:
            pages, table = self._pdf_pages(content)
        markdown = _prefer_page_text(markdown.translate(table), pages)
        return ParsedDocument(
            markdown_content=markdown,
            document_format=fmt,
            total_pages=len(pages),
            word_count=sum(1 for _ in _WORD_RE.finditer(markdown)),
            pages=pages,
        )

    def _to_markdown(self, content: bytes, fmt: DocumentFormat) -> str:
        """Convert bytes to Markdown via MarkItDown, forcing UTF-8.

        Mostly-ASCII text with occasional multibyte characters (smart quotes, accents)
        decodes instead of tripping MarkItDown's 'ascii' charset autodetect (guessed from
        the first 4 KiB), which failed such documents deep in the file. Genuinely-invalid
        bytes are sanitized (replaced) and retried.

        Args:
            content: The document's bytes.
            fmt: Its format.

        Returns:
            The Markdown.

        """
        stream_info_cls = cast(
            "type[_LazyStreamInfo]", importlib.import_module("markitdown").StreamInfo
        )

        info = stream_info_cls(extension=f".{fmt.value}", charset="utf-8")
        try:
            result = self._converter.convert_stream(io.BytesIO(content), stream_info=info)
        except UnicodeDecodeError:
            clean = content.decode("utf-8", errors="replace").encode("utf-8")
            result = self._converter.convert_stream(io.BytesIO(clean), stream_info=info)
        return str(result.text_content)

    @staticmethod
    def _pdf_pages(content: bytes) -> tuple[list[Page], dict[int, str]]:
        """Extract per-page text from a PDF via pymupdf; empty if extraction fails.

        Args:
            content: The PDF's bytes.

        Returns:
            The extracted pages and their glyph table; both empty if extraction fails.

        """
        try:
            with _open_pdf(stream=content, filetype="pdf") as doc:
                return _page_texts(doc)
        except Exception:  # PDF page extraction is best-effort supplementary data
            logger.exception("pdf_pages failed")
            return [], {}

    @staticmethod
    def page_count(path: str) -> int:
        """Count the pages of the PDF at ``path``; 0 when it is not a readable PDF.

        It never raises: 0 tells the caller to parse the document whole. Only pymupdf's
        open errors are caught (a truncated or non-PDF file).

        Args:
            path: The PDF file.

        Returns:
            The page count, or 0 for a non-PDF, a truncated PDF or a missing file.

        """
        try:
            with _open_pdf(path, filetype="pdf") as doc:
                return cast("_PdfDocument", doc).page_count
        except (RuntimeError, ValueError):
            return 0

    @staticmethod
    def parse_pages(
        path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse only pages ``first_page``..``last_page`` (1-based, inclusive) of a PDF.

        Pages keep their real position in the document (a range 2-3 yields pages numbered
        2 and 3), ``total_pages`` is the whole document's count, and
        ``markdown_content`` holds only the range's text, one block per page. The range's
        Private Use Area characters are replaced by their glyph names' text.

        Args:
            path: The PDF file.
            first_page: The first page of the range, from 1.
            last_page: The last page of the range, at most the document's page count.
            filename: Accepted for the ``PagedParser`` signature; unused here.

        Returns:
            The range as a parsed document.

        Raises:
            AppValueError: The range is empty, starts before page 1 or ends past the
                document's last page (``INVALID_INPUT``).

        """
        del filename
        with _open_pdf(path, filetype="pdf") as doc:
            pdf = cast("_PdfDocument", doc)
            total = pdf.page_count
            if first_page < 1 or last_page < first_page or last_page > total:
                msg = (
                    f"page range {first_page}-{last_page} is outside the document's "
                    f"{total} pages"
                )
                raise AppValueError(msg)
            texts = [
                str(pdf.load_page(n - 1).get_text())
                for n in range(first_page, last_page + 1)
            ]
            table = glyph_table(doc, "".join(texts))
            pages = [
                Page(page_number=n, text=text.translate(table))
                for n, text in zip(range(first_page, last_page + 1), texts, strict=True)
            ]
        markdown = "\n\n".join(page.text.strip() for page in pages)
        return ParsedDocument(
            markdown_content=markdown,
            document_format=DocumentFormat.PDF,
            total_pages=total,
            word_count=len(_WORD_RE.findall(markdown)),
            pages=pages,
        )

    @staticmethod
    def render_page(path: str, *, page_number: int, dpi: int) -> bytes:
        """Render one page of the PDF at ``path`` to PNG bytes.

        It feeds OCR of a page that has no text layer.

        Args:
            path: The PDF file.
            page_number: The page, from 1.
            dpi: The render resolution.

        Returns:
            The page as a PNG image.

        Raises:
            AppValueError: The page is outside the document (`INVALID_INPUT`).

        """
        with _open_pdf(path, filetype="pdf") as doc:
            pdf = cast("_PdfDocument", doc)
            if not 1 <= page_number <= pdf.page_count:
                msg = (
                    f"page {page_number} is outside the document's {pdf.page_count} pages"
                )
                raise AppValueError(msg)
            return pdf.load_page(page_number - 1).get_pixmap(dpi=dpi).tobytes("png")

    @staticmethod
    def render_glyphs(
        path: str, *, points: Collection[int], dpi: int
    ) -> dict[int, bytes]:
        """Render the first drawing of each code point in ``points`` to PNG bytes.

        It feeds OCR of a Private Use Area glyph that no glyph name spells: the image
        is the glyph's bounding box alone, so the model reads that glyph and not its
        neighbours. Pages are scanned in order, and a page whose plain text holds none
        of the code points still wanted is skipped before its characters are listed.

        Args:
            path: The PDF file.
            points: The code points to render.
            dpi: The render resolution.

        Returns:
            Code point → PNG, for each point drawn with a non-empty box; a point the
            document never draws is left out.

        """
        wanted = set(points)
        images: dict[int, bytes] = {}
        with _open_pdf(path, filetype="pdf") as doc:
            pdf = cast("_PdfDocument", doc)
            for number in range(pdf.page_count):
                if not wanted:
                    break
                page = pdf.load_page(number)
                if not wanted.intersection(map(ord, str(page.get_text()))):
                    continue
                for char, box in _page_chars(page):
                    clip = _clip(box, page.rect)
                    if ord(char) in wanted and clip is not None:
                        wanted.discard(ord(char))
                        pixmap = page.get_pixmap(dpi=dpi, clip=clip)
                        images[ord(char)] = pixmap.tobytes("png")
        return images

    def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path`` into a ``ParsedDocument``.

        Memory stays bounded.

        The large-document lane streams the object to disk and hands the parser a PATH, so
        the whole file never lands in memory: only a small header is read for format
        detection, the conversion streams from the open file (no in-memory copy), and PDF
        pages are extracted by opening the path. Never raises for a bad document — a
        failure is captured as ``error`` on the result.

        Args:
            path: The file's local path.
            declared: The MIME type the uploader declared; empty to detect it.
            filename: The original file name; empty uses the path's name.

        Returns:
            A document from a file ``path``.

        """
        head = self._read_header(path)
        fmt = detect_format(declared, filename, head)
        if fmt is DocumentFormat.UNKNOWN:
            return ParsedDocument(
                markdown_content="", document_format=fmt, error="unsupported_format"
            )
        if fmt is DocumentFormat.PDF and not has_pdf_header(head):
            return ParsedDocument(
                markdown_content="", document_format=fmt, error=_UNREADABLE_PDF
            )

        try:
            markdown = self._to_markdown_path(path, fmt)
        except Exception as exc:  # isolate one document's failure as error metadata
            logger.exception("parse_path failed")
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error=f"conversion_failed: {exc}",
            )

        pages, table = ([], {})
        if fmt is DocumentFormat.PDF:
            pages, table = self._pdf_pages_path(path)
        markdown = _prefer_page_text(markdown.translate(table), pages)
        return ParsedDocument(
            markdown_content=markdown,
            document_format=fmt,
            total_pages=len(pages),
            word_count=sum(1 for _ in _WORD_RE.finditer(markdown)),
            pages=pages,
        )

    @staticmethod
    def _read_header(path: str) -> bytes:
        """Read a small prefix of the file for magic-byte format detection.

        Never the whole file.

        Args:
            path: The file.

        Returns:
            A small prefix of the file for magic-byte format detection.

        """
        with Path(path).open("rb") as f:
            return f.read(_HEADER_BYTES)

    def _to_markdown_path(self, path: str, fmt: DocumentFormat) -> str:
        """Convert a file to Markdown by streaming the open file, forcing UTF-8.

        See _to_markdown. An invalid-byte failure is sanitized + retried only when the
        file fits _MAX_SANITIZE_BYTES, so the memory-bounded large lane never buffers a
        multi-GB file to recover a rare mis-encoding.

        Args:
            path: The file.
            fmt: Its format.

        Returns:
            A file to Markdown by streaming the open file, forcing UTF-8.

        Raises:
            UnicodeDecodeError: The converter could not decode the file as UTF-8.

        """
        stream_info_cls = cast(
            "type[_LazyStreamInfo]", importlib.import_module("markitdown").StreamInfo
        )

        info = stream_info_cls(extension=f".{fmt.value}", charset="utf-8")
        with Path(path).open("rb") as f:
            try:
                return str(
                    self._converter.convert_stream(f, stream_info=info).text_content
                )
            except UnicodeDecodeError:
                if Path(path).stat().st_size > _MAX_SANITIZE_BYTES:
                    raise
                _ = f.seek(0)
                clean = f.read().decode("utf-8", errors="replace").encode("utf-8")
                return str(
                    self._converter.convert_stream(
                        io.BytesIO(clean), stream_info=info
                    ).text_content
                )

    @staticmethod
    def _pdf_pages_path(path: str) -> tuple[list[Page], dict[int, str]]:
        """Extract per-page text from a PDF opened by path via pymupdf.

        ``[]`` on failure.

        Args:
            path: The PDF file.

        Returns:
            The extracted pages and their glyph table; both empty if extraction fails.

        """
        try:
            with _open_pdf(path, filetype="pdf") as doc:
                return _page_texts(doc)
        except Exception:  # PDF page extraction is best-effort supplementary data
            logger.exception("pdf_pages_path failed")
            return [], {}


def _items(node: Mapping[str, object], key: str) -> list[Mapping[str, object]]:
    """Return the child list ``key`` of one node of pymupdf's ``rawdict`` text.

    Args:
        node: A page, block, line or span of the ``rawdict``.
        key: The child list (``blocks``, ``lines``, ``spans``, ``chars``).

    Returns:
        The children; empty when the node has none (an image block has no lines).

    """
    return cast("list[Mapping[str, object]]", node.get(key, []))


def _page_chars(page: _PdfPage) -> list[tuple[str, Sequence[float]]]:
    """List every character a page draws with its bounding box, in reading order.

    Args:
        page: The page.

    Returns:
        (character, (x0, y0, x1, y1)) pairs.

    """
    option = "rawdict"  # each character with its bounding box
    raw = cast("Mapping[str, object]", page.get_text(option))
    return [
        (cast("str", char["c"]), cast("Sequence[float]", char["bbox"]))
        for block in _items(raw, "blocks")
        for line in _items(block, "lines")
        for span in _items(line, "spans")
        for char in _items(span, "chars")
    ]


def _clip(
    box: Sequence[float], bounds: Sequence[float]
) -> tuple[float, float, float, float] | None:
    """Intersect a glyph's box with the page's bounds.

    Args:
        box: The glyph's (x0, y0, x1, y1).
        bounds: The page's (x0, y0, x1, y1).

    Returns:
        The intersection, or ``None`` when it is empty (nothing to render).

    """
    x0, y0 = max(box[0], bounds[0]), max(box[1], bounds[1])
    x1, y1 = min(box[2], bounds[2]), min(box[3], bounds[3])
    return (x0, y0, x1, y1) if x0 < x1 and y0 < y1 else None
