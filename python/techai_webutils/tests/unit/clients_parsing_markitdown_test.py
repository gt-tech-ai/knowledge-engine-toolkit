"""Tests for MarkItDownParser (unified Markdown conversion + pymupdf PDF page extraction)."""

from dataclasses import dataclass
from pathlib import Path

import pymupdf

from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser
from techai_webutils.core.domain import DocumentFormat


def _make_pdf(*lines: str) -> bytes:
    """Build an in-memory PDF with one page per line of text."""
    doc = pymupdf.open()
    for line in lines:
        page = doc.new_page()
        page.insert_text((72, 72), line)
    return doc.tobytes()


@dataclass
class _Converted:
    """A MarkItDown-shaped conversion result carrying only ``text_content``."""

    text_content: str


class _FailingConverter:
    """A MarkItDown-shaped converter that always raises (drives the graceful-error path)."""

    def convert_stream(self, stream: object, **kwargs: object) -> _Converted:  # noqa: ARG002
        msg = "boom"
        raise RuntimeError(msg)


class _FixedMarkdownConverter:
    """A converter that returns fixed Markdown regardless of input (isolates the page-extract path)."""

    def convert_stream(self, stream: object, **kwargs: object) -> _Converted:  # noqa: ARG002
        return _Converted(text_content="converted markdown")


class TestMarkItDownParser:
    """Tests for the MarkItDown parser."""

    def test_pdf_converts_and_extracts_pages(self) -> None:
        """Test that a PDF converts to Markdown and yields per-page text for provenance.

        **Why this test is important:**
          - PDF page-level extraction is what makes citations point at a page; losing pages breaks
            provenance, the core retrieval-trust feature.

        **What it tests:**
          - A 2-page PDF parses ok, reports total_pages=2 with 1-based page numbers + text, a
            positive word count, and Markdown containing the content.
        """
        parser = MarkItDownParser()
        result = parser.parse(_make_pdf("Alpha page", "Bravo page"), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.total_pages == 2
        assert [p.page_number for p in result.pages] == [1, 2]
        assert "Alpha" in result.pages[0].text
        assert "Bravo" in result.pages[1].text
        assert result.word_count > 0
        assert "Alpha" in result.markdown_content

    def test_html_converts_to_markdown(self) -> None:
        """Test that HTML converts to Markdown with no page extraction.

        **Why this test is important:**
          - HTML is a common connector format; heading structure must survive into Markdown, and
            non-PDF formats must not fabricate pages.

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

    def test_csv_converts_to_table(self) -> None:
        """Test that CSV content is converted (values preserved) to Markdown.

        **Why this test is important:**
          - Tabular connectors (CSV/XLSX) are common; dropping cell values would make the document
            unsearchable.

        **What it tests:**
          - A CSV cell value survives into the Markdown output.
        """
        parser = MarkItDownParser()
        result = parser.parse(b"name,age\nAda,36\n", filename="p.csv")
        assert result.ok
        assert result.document_format is DocumentFormat.CSV
        assert "Ada" in result.markdown_content

    def test_mostly_ascii_text_with_smart_quote_past_4kib_is_indexed(self) -> None:
        """Test that mostly-ASCII UTF-8 text with a multibyte character past the 4 KiB sniff window parses.

        **Why this test is important:**
          - MarkItDown guesses the charset from a file's first 4096 bytes; a document that is almost
            entirely ASCII with an occasional smart quote / accent (every real prose document) was
            misdetected as 'ascii' and strict-decoded, failing ingestion deep in the file. Forcing
            UTF-8 must fix it.

        **What it tests:**
          - 5000 ASCII bytes followed by a UTF-8 smart-quoted phrase parses ok and the phrase survives.
        """
        content = ("a" * 5000 + " he said “hello”").encode("utf-8")
        result = MarkItDownParser().parse(content, filename="book.txt")
        assert result.ok, result.error
        assert result.document_format is DocumentFormat.TXT
        assert "“hello”" in result.markdown_content

    def test_invalid_utf8_bytes_recovered_via_lenient_fallback(self) -> None:
        """Test that genuinely-invalid UTF-8 bytes degrade gracefully (replaced), not fail ingestion.

        **Why this test is important:**
          - Forcing UTF-8 fixes the mostly-ASCII misdetection but still strict-decodes; a document with
            a truly invalid byte must be recovered via replacement rather than recorded failed (L3).

        **What it tests:**
          - Content with an invalid byte (0xff) plus readable text parses ok and keeps the readable text.
        """
        content = b"valid text before " + b"\xff\xfe" + b" and readable text after"
        result = MarkItDownParser().parse(content, filename="broken.txt")
        assert result.ok, result.error
        assert "readable text after" in result.markdown_content

    def test_streaming_path_forces_utf8(self, tmp_path: Path) -> None:
        """Test that the streaming (large-file) path also forces UTF-8.

        **Why this test is important:**
          - Large documents take the streaming ``parse_path`` route; the same charset fix must apply
            there so a large mostly-ASCII book with smart quotes/accents isn't failed.

        **What it tests:**
          - A file with a multibyte character past 4 KiB parses ok via parse_path and the text survives.
        """
        p = tmp_path / "big.txt"
        p.write_bytes(("a" * 5000 + " café — done").encode("utf-8"))
        result = MarkItDownParser().parse_path(str(p), filename="big.txt")
        assert result.ok, result.error
        assert "café" in result.markdown_content

    def test_unsupported_format_returns_error(self) -> None:
        """Test that an undetectable format returns an error result (no raise).

        **Why this test is important:**
          - Unknown formats must be surfaced as a permanent failure, not crash the worker or be
            silently mis-parsed.

        **What it tests:**
          - Non-magic, extension-less bytes yield error == 'unsupported_format'.
        """
        parser = MarkItDownParser()
        result = parser.parse(b"\x00\x01\x02", filename="mystery")
        assert not result.ok
        assert result.error == "unsupported_format"

    def test_conversion_failure_is_captured_not_raised(self) -> None:
        """Test that a converter exception becomes error metadata instead of propagating.

        **Why this test is important:**
          - A crashing/corrupt document must fail the single document gracefully (error metadata),
            never take down the parse worker or the batch.

        **What it tests:**
          - An injected failing converter yields not-ok with the failure captured in error.
        """
        parser = MarkItDownParser(converter=_FailingConverter())
        result = parser.parse(b"%PDF-1.7\nnot-real", filename="bad.pdf")
        assert result.document_format is DocumentFormat.PDF
        assert not result.ok
        assert "boom" in result.error

    def test_pdf_page_extraction_failure_is_best_effort(self) -> None:
        """Test that a PDF whose page extraction fails still returns ok Markdown with empty pages.

        **Why this test is important:**
          - pymupdf page extraction is supplementary provenance; if it fails on a document the
            converter *could* read but pymupdf can't open, the parse must still succeed (Markdown)
            with pages omitted — never fail a document over best-effort supplementary data.

        **What it tests:**
          - %PDF- content the (injected) converter accepts but pymupdf cannot open yields ok, PDF
            format, and an empty pages list.
        """
        parser = MarkItDownParser(converter=_FixedMarkdownConverter())
        result = parser.parse(b"%PDF-1.7\nnot-a-real-pdf-body", filename="x.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.pages == []
        assert "converted markdown" in result.markdown_content

    def test_parse_path_reads_pdf_from_disk(self, tmp_path: Path) -> None:
        """Test that parse_path parses a PDF from a file path (bounded memory) like the bytes path.

        **Why this test is important:**
          - The large-document lane streams the object to disk and hands the parser a PATH so the whole
            file never lands in memory; parse_path must produce the same structured result as parse.

        **What it tests:**
          - A 2-page PDF written to disk parses ok via parse_path with total_pages=2 and its content
            in the Markdown.
        """
        pdf = tmp_path / "doc.pdf"
        pdf.write_bytes(_make_pdf("Alpha page", "Bravo page"))
        parser = MarkItDownParser()
        result = parser.parse_path(str(pdf), filename="doc.pdf")
        assert result.ok
        assert result.document_format is DocumentFormat.PDF
        assert result.total_pages == 2
        assert "Alpha" in result.markdown_content
