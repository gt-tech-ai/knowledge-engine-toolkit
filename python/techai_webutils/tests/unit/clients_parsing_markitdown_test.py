"""Tests for MarkItDownParser.

Unified Markdown conversion plus pymupdf PDF page extraction.
"""

from dataclasses import dataclass
from pathlib import Path

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser
from techai_webutils.core.domain import DocumentFormat
from techai_webutils.core.errors import AppValueError

_EXPECTED_TOTAL_PAGES = 2
_THREE_PAGES = 3
_TWO_WORDS = 2


def _make_pdf(*lines: str) -> bytes:
    """Build an in-memory PDF with one page per line of text.

    Args:
        *lines: The page's lines.

    Returns:
        An in-memory PDF with one page per line of text.

    """
    doc = pymupdf.open()
    for line in lines:
        page = doc.new_page()
        _ = page.insert_text((72, 72), line)
    return doc.tobytes()


@dataclass
class _Converted:
    """A MarkItDown-shaped conversion result carrying only ``text_content``."""

    text_content: str
    """The converted Markdown."""


class _FailingConverter:
    """A MarkItDown-shaped converter that always raises.

    It drives the graceful-error path.
    """

    @staticmethod
    def convert_stream(stream: object, **kwargs: object) -> _Converted:
        del stream, kwargs
        msg = "boom"
        raise RuntimeError(msg)


class _FixedMarkdownConverter:
    """A converter that returns fixed Markdown regardless of input.

    It isolates the page-extract path.
    """

    @staticmethod
    def convert_stream(stream: object, **kwargs: object) -> _Converted:
        del stream, kwargs
        return _Converted(text_content="converted markdown")


class TestMarkItDownParser:
    """Tests for the MarkItDown parser."""

    @staticmethod
    def test_pdf_converts_and_extracts_pages() -> None:
        """Test that a PDF converts to Markdown and yields per-page text for provenance.

        **Why this test is important:**
          - PDF page-level extraction is what makes citations point at a page; losing
            pages breaks provenance, the core retrieval-trust feature.

        **What it tests:**
          - A 2-page PDF parses ok, reports total_pages=2 with 1-based page numbers +
            text, a positive word count, and Markdown containing the content.
        """
        parser = MarkItDownParser()
        result = parser.parse(_make_pdf("Alpha page", "Bravo page"), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.total_pages == _EXPECTED_TOTAL_PAGES
        assert [p.page_number for p in result.pages] == [1, 2]
        assert "Alpha" in result.pages[0].text
        assert "Bravo" in result.pages[1].text
        assert result.word_count > 0
        assert "Alpha" in result.markdown_content

    @staticmethod
    def test_html_converts_to_markdown() -> None:
        """Test that HTML converts to Markdown with no page extraction.

        **Why this test is important:**
          - HTML is a common connector format; heading structure must survive into
            Markdown, and non-PDF formats must not fabricate pages.

        **What it tests:**
          - <h1> becomes '# Title' and pages is empty for a non-PDF.
        """
        parser = MarkItDownParser()
        result = parser.parse(
            b"<html><body><h1>Title</h1><p>Body</p></body></html>", filename="p.html"
        )
        assert result.ok
        assert result.document_format is DocumentFormat.HTML
        assert "# Title" in result.markdown_content
        assert result.pages == []

    @staticmethod
    def test_csv_converts_to_table() -> None:
        """Test that CSV content is converted (values preserved) to Markdown.

        **Why this test is important:**
          - Tabular connectors (CSV/XLSX) are common; dropping cell values would make the
            document unsearchable.

        **What it tests:**
          - A CSV cell value survives into the Markdown output.
        """
        parser = MarkItDownParser()
        result = parser.parse(b"name,age\nAda,36\n", filename="p.csv")
        assert result.ok
        assert result.document_format is DocumentFormat.CSV
        assert "Ada" in result.markdown_content

    @staticmethod
    def test_mostly_ascii_text_with_smart_quote_past_4kib_is_indexed() -> None:
        """Test that UTF-8 with a multibyte character past the sniff window parses.

        The text is mostly ASCII; the window is 4 KiB.

        **Why this test is important:**
          - MarkItDown guesses the charset from a file's first 4096 bytes; a document that
            is almost entirely ASCII with an occasional smart quote / accent (every real
            prose document) was misdetected as 'ascii' and strict-decoded, failing
            ingestion deep in the file. Forcing UTF-8 must fix it.

        **What it tests:**
          - 5000 ASCII bytes followed by a UTF-8 smart-quoted phrase parses ok and the
            phrase survives.
        """
        content = ("a" * 5000 + " he said “hello”").encode("utf-8")
        result = MarkItDownParser().parse(content, filename="book.txt")
        assert result.ok, result.error
        assert result.document_format is DocumentFormat.TXT
        assert "“hello”" in result.markdown_content

    @staticmethod
    def test_invalid_utf8_bytes_recovered_via_lenient_fallback() -> None:
        """Test that genuinely-invalid UTF-8 bytes degrade gracefully.

        They are replaced and do not fail ingestion.

        **Why this test is important:**
          - Forcing UTF-8 fixes the mostly-ASCII misdetection but still strict-decodes; a
            document with a truly invalid byte must be recovered via replacement rather
            than recorded failed (L3).

        **What it tests:**
          - Content with an invalid byte (0xff) plus readable text parses ok and keeps the
            readable text.
        """
        content = b"valid text before " + b"\xff\xfe" + b" and readable text after"
        result = MarkItDownParser().parse(content, filename="broken.txt")
        assert result.ok, result.error
        assert "readable text after" in result.markdown_content

    @staticmethod
    def test_streaming_path_forces_utf8(tmp_path: Path) -> None:
        """Test that the streaming (large-file) path also forces UTF-8.

        **Why this test is important:**
          - Large documents take the streaming ``parse_path`` route; the same charset fix
            must apply there so a large mostly-ASCII book with smart quotes/accents isn't
            failed.

        **What it tests:**
          - A file with a multibyte character past 4 KiB parses ok via parse_path and the
            text survives.

        Args:
            tmp_path: A fresh directory for the test.

        """
        p = tmp_path / "big.txt"
        _ = p.write_bytes(("a" * 5000 + " café — done").encode("utf-8"))
        result = MarkItDownParser().parse_path(str(p), filename="big.txt")
        assert result.ok, result.error
        assert "café" in result.markdown_content

    @staticmethod
    def test_unsupported_format_returns_error() -> None:
        """Test that an undetectable format returns an error result (no raise).

        **Why this test is important:**
          - Unknown formats must be surfaced as a permanent failure, not crash the worker
            or be silently mis-parsed.

        **What it tests:**
          - Non-magic, extension-less bytes yield error == 'unsupported_format'.
        """
        parser = MarkItDownParser()
        result = parser.parse(b"\x00\x01\x02", filename="mystery")
        assert not result.ok
        assert result.error == "unsupported_format"

    @staticmethod
    def test_conversion_failure_is_captured_not_raised() -> None:
        """Test that a converter exception becomes error metadata instead of propagating.

        **Why this test is important:**
          - A crashing/corrupt document must fail the single document gracefully (error
            metadata), never take down the parse worker or the batch.

        **What it tests:**
          - An injected failing converter yields not-ok with the failure captured in
            error.
        """
        parser = MarkItDownParser(converter=_FailingConverter())
        result = parser.parse(b"%PDF-1.7\nnot-real", filename="bad.pdf")
        assert result.document_format is DocumentFormat.PDF
        assert not result.ok
        assert "boom" in result.error

    @staticmethod
    def test_pdf_page_extraction_failure_is_best_effort() -> None:
        """Test that a PDF whose page extraction fails still returns ok Markdown.

        Its pages are empty.

        **Why this test is important:**
          - pymupdf page extraction is supplementary provenance; if it fails on a document
            the converter *could* read but pymupdf can't open, the parse must still
            succeed (Markdown) with pages omitted — never fail a document over best-effort
            supplementary data.

        **What it tests:**
          - %PDF- content the (injected) converter accepts but pymupdf cannot open yields
            ok, PDF format, and an empty pages list.
        """
        parser = MarkItDownParser(converter=_FixedMarkdownConverter())
        result = parser.parse(b"%PDF-1.7\nnot-a-real-pdf-body", filename="x.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.pages == []
        assert "converted markdown" in result.markdown_content

    @staticmethod
    def test_declared_pdf_without_pdf_header_fails_conversion() -> None:
        """Test that bytes declared a PDF but lacking its header fail, not index as text.

        **Why this test is important:**
          - The declared type wins format detection when the magic is absent, so a
            text body uploaded as ``application/pdf`` used to convert as plain text and
            index as if it were the PDF; the uploader must instead see the document fail.

        **What it tests:**
          - Non-PDF bytes declared ``application/pdf`` yield not-ok, PDF format, empty
            Markdown and a ``conversion_failed`` error, even with a converter that would
            accept them.
        """
        parser = MarkItDownParser(converter=_FixedMarkdownConverter())
        result = parser.parse(
            b"this is not a pdf", declared="application/pdf", filename="bogus.pdf"
        )
        assert not result.ok
        assert result.document_format is DocumentFormat.PDF
        assert not result.markdown_content
        assert result.error.startswith("conversion_failed")

    @staticmethod
    def test_parse_path_declared_pdf_without_pdf_header_fails(tmp_path: Path) -> None:
        """Test that the path lane refuses a declared PDF that lacks the PDF header.

        **Why this test is important:**
          - The large lane parses from a path; it must reach the same verdict as the
            bytes lane, or a bogus PDF over the large-lane threshold would still index.

        **What it tests:**
          - A non-PDF file declared ``application/pdf`` yields not-ok, PDF format and a
            ``conversion_failed`` error via parse_path.

        Args:
            tmp_path: A fresh directory for the test.

        """
        bogus = tmp_path / "bogus.pdf"
        _ = bogus.write_bytes(b"this is not a pdf")
        parser = MarkItDownParser(converter=_FixedMarkdownConverter())
        result = parser.parse_path(
            str(bogus), declared="application/pdf", filename="bogus.pdf"
        )
        assert not result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.error.startswith("conversion_failed")

    @staticmethod
    def test_parse_path_reads_pdf_from_disk(tmp_path: Path) -> None:
        """Test that parse_path parses a PDF from a file path like the bytes path.

        Memory stays bounded.

        **Why this test is important:**
          - The large-document lane streams the object to disk and hands the parser a PATH
            so the whole file never lands in memory; parse_path must produce the same
            structured result as parse.

        **What it tests:**
          - A 2-page PDF written to disk parses ok via parse_path with total_pages=2 and
            its content in the Markdown.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "doc.pdf"
        _ = pdf.write_bytes(_make_pdf("Alpha page", "Bravo page"))
        parser = MarkItDownParser()
        result = parser.parse_path(str(pdf), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.total_pages == _EXPECTED_TOTAL_PAGES
        assert "Alpha" in result.markdown_content


class TestMarkItDownPaging:
    """Tests for the page-range entries (``page_count`` / ``parse_pages``)."""

    @staticmethod
    def test_page_count_counts_pdf_pages(tmp_path: Path) -> None:
        """Test that ``page_count`` reports a PDF's pages without parsing it.

        **Why this test is important:**
          - The caller cuts a large PDF into page ranges from this count; a wrong count
            drops or duplicates pages in the units built from the parts.

        **What it tests:**
          - A 3-page PDF on disk counts 3.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "doc.pdf"
        _ = pdf.write_bytes(_make_pdf("One", "Two", "Three"))
        assert MarkItDownParser().page_count(str(pdf)) == _THREE_PAGES

    @staticmethod
    def test_page_count_is_zero_for_unreadable_files(tmp_path: Path) -> None:
        """Test that a non-PDF or truncated PDF counts 0 instead of raising.

        **Why this test is important:**
          - 0 tells the caller to parse the document whole; an exception would fail a
            document that the whole-file parse can still handle.

        **What it tests:**
          - A text file and a PDF cut mid-file both count 0, and a missing path too.

        Args:
            tmp_path: A fresh directory for the test.

        """
        text = tmp_path / "note.txt"
        _ = text.write_text("plain text")
        cut = tmp_path / "cut.pdf"
        _ = cut.write_bytes(_make_pdf("One", "Two")[:200])
        parser = MarkItDownParser()
        assert parser.page_count(str(text)) == 0
        assert parser.page_count(str(cut)) == 0
        assert parser.page_count(str(tmp_path / "missing.pdf")) == 0

    @staticmethod
    def test_parse_pages_numbers_pages_by_position(tmp_path: Path) -> None:
        """Test that a range keeps each page's real number and only the range's text.

        **Why this test is important:**
          - A unit built from pages 2-3 must cite pages 2-3 of the original; renumbering
            from 1 would point citations at the wrong page.

        **What it tests:**
          - Range 2-3 of a 3-page PDF yields pages numbered 2 and 3, the document's real
            total, and Markdown holding those pages' text but not page 1's.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "doc.pdf"
        _ = pdf.write_bytes(_make_pdf("Alpha", "Bravo", "Charlie"))
        result = MarkItDownParser().parse_pages(
            str(pdf), first_page=2, last_page=3, filename="doc.pdf"
        )
        assert result.ok
        assert [p.page_number for p in result.pages] == [2, 3]
        assert result.total_pages == _THREE_PAGES
        assert "Bravo" in result.markdown_content
        assert "Charlie" in result.markdown_content
        assert "Alpha" not in result.markdown_content
        assert result.word_count == _TWO_WORDS

    @staticmethod
    @pytest.mark.parametrize(("first", "last"), [(0, 1), (3, 2), (3, 4)])
    def test_parse_pages_rejects_a_bad_range(
        tmp_path: Path, first: int, last: int
    ) -> None:
        """Test that an out-of-range request raises a coded error naming the range.

        **Why this test is important:**
          - A caller that miscounts must fail loudly, not receive a silently short part.

        **What it tests:**
          - Ranges 0-1, 3-2 and 3-4 of a 3-page PDF raise ``AppValueError`` with the
            range and the page count in the message.

        Args:
            tmp_path: A fresh directory for the test.
            first: The first page requested.
            last: The last page requested.

        """
        pdf = tmp_path / "doc.pdf"
        _ = pdf.write_bytes(_make_pdf("Alpha", "Bravo", "Charlie"))
        with pytest.raises(AppValueError, match=rf"{first}-{last}.*3 pages"):
            _ = MarkItDownParser().parse_pages(str(pdf), first_page=first, last_page=last)
