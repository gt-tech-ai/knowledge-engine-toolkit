"""MarkItDown-based document parser with pymupdf PDF page extraction.

Microsoft's ``markitdown`` is the single unified conversion engine (all formats ->
Markdown); ``pymupdf`` runs *alongside* it for PDFs only, extracting page-by-page text +
boundaries for citation provenance. The parser never raises for a bad document: a
conversion failure is captured as ``error`` on the returned ``ParsedDocument`` so a single
poison document fails alone.
"""

from __future__ import annotations

import io
import re
from pathlib import Path
from typing import BinaryIO, Protocol, cast

from techai_webutils.clients.parsing.isolated.detect import detect_format
from techai_webutils.core.domain import DocumentFormat, Page, ParsedDocument

# _HEADER_BYTES is how much of a file's prefix is read for magic-byte format detection on the
# path (streaming) lane — enough for the format sniffers, far less than the whole file.
_HEADER_BYTES = 4096
"""Bytes of a file's prefix read for magic-byte format detection on the streaming lane."""

# _WORD_RE matches runs of non-whitespace. Counting matches avoids materializing a full token list:
# ``len(text.split())`` allocates millions of small ``str`` objects for a large document just to
# take their length. ``pymupdf`` and ``markitdown`` are imported lazily inside the parse
# methods (not at module top) so the PARENT process that imports this class for the port never pays
# their heavy import cost — only a subprocess that actually parses does.
_WORD_RE = re.compile(r"\S+")
"""Matches runs of non-whitespace; counting matches avoids materializing a full token list."""

# _MAX_SANITIZE_BYTES bounds the lenient-decode fallback on the streaming (large) lane: a file whose
# bytes are not valid UTF-8 even after the forced-charset pass is re-read + sanitized only when it fits
# this cap, so the memory-bounded large lane never buffers a multi-GB file to recover a rare
# mis-encoding. Above the cap the original UnicodeDecodeError surfaces (recorded as conversion_failed).
_MAX_SANITIZE_BYTES = 64 << 20  # 64 MiB
"""Cap (64 MiB) on the lenient-decode fallback so the large lane never buffers a multi-GB file to recover."""


class _ConversionResult(Protocol):
    """The slice of MarkItDown's result the parser reads."""

    text_content: str
    """The Markdown text MarkItDown produced from the source document."""


class _Converter(Protocol):
    """The slice of the MarkItDown API the parser depends on (injectable for tests)."""

    def convert_stream(self, stream: BinaryIO, **kwargs: object) -> _ConversionResult:
        """Convert a binary stream (in-memory or an open file) to a result carrying ``text_content``."""
        ...


class _PdfPage(Protocol):
    """The slice of a pymupdf ``Page`` the parser reads."""

    def get_text(self) -> object:
        """Return the page's plain text (pymupdf types it as ``str | list | dict``)."""
        ...


class _PdfDocument(Protocol):
    """The slice of a pymupdf ``Document`` the parser reads (pymupdf leaves these untyped)."""

    @property
    def page_count(self) -> int:
        """Number of pages in the document."""
        ...

    def load_page(self, page_id: int) -> _PdfPage:
        """Load the zero-based page ``page_id``."""
        ...


def _page_texts(doc: object) -> list[Page]:
    """Extract per-page text from an open pymupdf document, numbering pages from 1.

    Iterates by index: pymupdf's ``Document`` has no typed ``__iter__``, and its ``page_count`` /
    ``load_page`` are untyped, so the document is read through the ``_PdfDocument`` view.
    """
    pdf = cast("_PdfDocument", doc)
    return [
        Page(page_number=index + 1, text=str(pdf.load_page(index).get_text()))
        for index in range(pdf.page_count)
    ]


class MarkItDownParser:
    """Parse document bytes into Markdown + (for PDFs) per-page text.

    The ``converter`` is injectable so the error path can be exercised deterministically;
    it defaults to a real ``MarkItDown`` instance.
    """

    def __init__(self, converter: _Converter | None = None) -> None:
        """Bind the MarkItDown converter (defaults to a fresh ``MarkItDown``)."""
        if converter is not None:
            self._converter: _Converter = converter
            return
        # Lazy import: the default converter is the only place markitdown is needed, and it is
        # constructed in the parse subprocess — the parent importing this class must not pay the import.
        # MarkItDown's convert_stream keyword params (stream_info/file_extension) are narrower than the
        # Protocol's **kwargs, so it is not *structurally* assignable — cast the concrete default.
        from markitdown import MarkItDown  # noqa: PLC0415 — lazy: the parent never loads markitdown

        self._converter = cast("_Converter", MarkItDown())

    def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content`` into a ``ParsedDocument`` (never raises for a bad document)."""
        fmt = detect_format(declared, filename, content)
        if fmt is DocumentFormat.UNKNOWN:
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error="unsupported_format",
            )

        try:
            markdown = self._to_markdown(content, fmt)
        except Exception as exc:  # isolate one document's failure as error metadata
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error=f"conversion_failed: {exc}",
            )

        pages = self._pdf_pages(content) if fmt is DocumentFormat.PDF else []
        return ParsedDocument(
            markdown_content=markdown,
            document_format=fmt,
            total_pages=len(pages),
            word_count=sum(1 for _ in _WORD_RE.finditer(markdown)),
            pages=pages,
        )

    def _to_markdown(self, content: bytes, fmt: DocumentFormat) -> str:
        """Convert bytes to Markdown via MarkItDown, forcing UTF-8.

        Mostly-ASCII text with occasional multibyte characters (smart quotes, accents) decodes instead
        of tripping MarkItDown's 'ascii' charset autodetect (guessed from the first 4 KiB), which failed
        such documents deep in the file. Genuinely-invalid bytes are sanitized (replaced) and retried.
        """
        from markitdown import StreamInfo  # noqa: PLC0415 — lazy: the parent never loads markitdown

        info = StreamInfo(extension=f".{fmt.value}", charset="utf-8")
        try:
            result = self._converter.convert_stream(io.BytesIO(content), stream_info=info)
        except UnicodeDecodeError:
            clean = content.decode("utf-8", errors="replace").encode("utf-8")
            result = self._converter.convert_stream(io.BytesIO(clean), stream_info=info)
        return str(result.text_content)

    @staticmethod
    def _pdf_pages(content: bytes) -> list[Page]:
        """Extract per-page text from a PDF via pymupdf; return [] if extraction fails."""
        import pymupdf  # noqa: PLC0415 — lazy so the parent process never loads pymupdf

        try:
            with pymupdf.open(stream=content, filetype="pdf") as doc:
                return _page_texts(doc)
        except Exception:  # PDF page extraction is best-effort supplementary data
            return []

    def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path`` (bounded memory) into a ``ParsedDocument``.

        The large-document lane streams the object to disk and hands the parser a PATH, so the whole
        file never lands in memory: only a small header is read for format detection, the conversion
        streams from the open file (no in-memory copy), and PDF pages are extracted by opening the
        path. Never raises for a bad document — a failure is captured as ``error`` on the result.
        """
        fmt = detect_format(declared, filename, self._read_header(path))
        if fmt is DocumentFormat.UNKNOWN:
            return ParsedDocument(
                markdown_content="", document_format=fmt, error="unsupported_format"
            )

        try:
            markdown = self._to_markdown_path(path, fmt)
        except Exception as exc:  # isolate one document's failure as error metadata
            return ParsedDocument(
                markdown_content="",
                document_format=fmt,
                error=f"conversion_failed: {exc}",
            )

        pages = self._pdf_pages_path(path) if fmt is DocumentFormat.PDF else []
        return ParsedDocument(
            markdown_content=markdown,
            document_format=fmt,
            total_pages=len(pages),
            word_count=sum(1 for _ in _WORD_RE.finditer(markdown)),
            pages=pages,
        )

    @staticmethod
    def _read_header(path: str) -> bytes:
        """Read a small prefix of the file for magic-byte format detection (not the whole file)."""
        with Path(path).open("rb") as f:
            return f.read(_HEADER_BYTES)

    def _to_markdown_path(self, path: str, fmt: DocumentFormat) -> str:
        """Convert a file to Markdown by streaming the open file, forcing UTF-8.

        See _to_markdown. An invalid-byte failure is sanitized + retried only when the file fits
        _MAX_SANITIZE_BYTES, so the memory-bounded large lane never buffers a multi-GB file to recover a
        rare mis-encoding.
        """
        from markitdown import StreamInfo  # noqa: PLC0415 — lazy: the parent never loads markitdown

        info = StreamInfo(extension=f".{fmt.value}", charset="utf-8")
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
    def _pdf_pages_path(path: str) -> list[Page]:
        """Extract per-page text from a PDF opened by path via pymupdf; return [] on failure."""
        import pymupdf  # noqa: PLC0415 — lazy so the parent process never loads pymupdf

        try:
            with pymupdf.open(path, filetype="pdf") as doc:
                return _page_texts(doc)
        except Exception:  # PDF page extraction is best-effort supplementary data
            return []
