"""Tests for MarkItDownParser.

Unified Markdown conversion plus pymupdf PDF page extraction.
"""

import sys
from dataclasses import dataclass
from pathlib import Path
from unittest.mock import Mock

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.markitdown_parser import MarkItDownParser
from techai_webutils.core.domain import DocumentFormat
from techai_webutils.core.errors import AppValueError
from tests.pua_pdf import pua_pdf

_EXPECTED_TOTAL_PAGES = 2
_THREE_PAGES = 3
_TWO_WORDS = 2
_BOOK_WORDS = 10

_PUA_TEXT = "fa\ue053 \uf761"
"""A ligature and a legacy small cap at Private Use Area code points: "fact a"."""

_PUA_CMAP = {
    0x20: "space",
    0x61: "a",
    0x66: "f",
    0xE053: "c_t",
    0xF761: "uniF761",
}
"""The probe font's names: ``c_t`` spells "ct"; ``uniF761`` spells nothing, so the
legacy glyph list's ``Asmall`` names it."""


def _assert_glyphs_spelled(text: str) -> None:
    """Assert ``text`` holds the PUA sample spelled out and no PUA character.

    Args:
        text: Parsed text.

    """
    assert "fact a" in text
    assert not any("\ue000" <= c <= "\uf8ff" for c in text)


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


def _converter_returning(markdown: str) -> Mock:
    """Build a mock converter whose every conversion yields ``markdown``.

    Args:
        markdown: The Markdown each conversion returns.

    Returns:
        The mock converter.

    """
    return Mock(
        spec=["convert_stream"],
        convert_stream=Mock(return_value=_Converted(text_content=markdown)),
    )


class TestMarkItDownParser:
    """Tests for the MarkItDown parser."""

    @staticmethod
    @pytest.mark.parametrize("lane", ["bytes", "path"])
    def test_pdf_markdown_without_word_spacing_falls_back_to_page_text(
        lane: str, tmp_path: Path
    ) -> None:
        """Test that a PDF whose Markdown lost its word spacing indexes the page text.

        **Why this test is important:**
          - MarkItDown extracts a whole PDF with pdfplumber when one page looks like a
            form, and pdfplumber drops the spaces of tightly typeset text: a typeset book
            indexed about half its words as run-together tokens that match no query,
            while still reporting a successful parse.

        **What it tests:**
          - On both the bytes and the path lane, Markdown with the page text's words run
            together is replaced by the pymupdf page text, and the word count is the
            page text's.

        Args:
            lane: Which entry point parses the PDF.
            tmp_path: A fresh directory for the path lane.

        """
        content = _make_pdf("Call me Ishmael some years ago", "never mind how long")
        parser = MarkItDownParser(
            converter=_converter_returning(
                "CallmeIshmaelsomeyearsago\n\nnevermindhowlong"
            )
        )
        if lane == "bytes":
            result = parser.parse(content, filename="book.pdf")
        else:
            pdf = tmp_path / "book.pdf"
            _ = pdf.write_bytes(content)
            result = parser.parse_path(str(pdf), filename="book.pdf")
        assert result.ok
        assert result.markdown_content == (
            "Call me Ishmael some years ago\n\nnever mind how long"
        )
        assert result.word_count == _BOOK_WORDS

    @staticmethod
    def test_pdf_markdown_with_its_words_is_kept() -> None:
        """Test that PDF Markdown carrying the page text's words is kept as converted.

        **Why this test is important:**
          - MarkItDown's Markdown keeps structure the page text lacks (tables as
            Markdown tables); the fallback must replace it only when words were lost.

        **What it tests:**
          - Markdown holding the page text's words plus table syntax is returned
            unchanged.
        """
        markdown = "| Alpha | page |\n| --- | --- |\n\nBravo page"
        parser = MarkItDownParser(converter=_converter_returning(markdown))
        result = parser.parse(_make_pdf("Alpha page", "Bravo page"), filename="t.pdf")
        assert result.markdown_content == markdown

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

    @staticmethod
    def test_pdf_pua_glyphs_are_spelled_in_markdown_and_pages() -> None:
        """Test that parse replaces a PDF's PUA glyphs with the text their names spell.

        **Why this test is important:**
          - Books set "ct" and "Th" ligatures and small caps at PUA code points; left in
            place, "fact" indexes as "fa" plus a character no query or embedder matches.

        **What it tests:**
          - The Markdown and every page read "fact a", with no PUA character left.
        """
        result = MarkItDownParser().parse(
            pua_pdf(_PUA_TEXT, _PUA_CMAP), filename="book.pdf"
        )
        assert result.ok
        _assert_glyphs_spelled(result.markdown_content)
        _assert_glyphs_spelled(result.pages[0].text)

    @staticmethod
    def test_pdf_without_fonttools_keeps_its_pages(
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Test that a PUA PDF parsed with no fontTools keeps its text as parsed.

        **Why this test is important:**
          - fontTools ships in the ``parsing`` extra; a consumer that installs markitdown
            and pymupdf on its own lacks it. Page extraction is best-effort, so an
            import error there would silently drop every page of the document.

        **What it tests:**
          - With ``fontTools.agl`` unimportable, the parse succeeds and its page still
            holds the text, PUA characters included.

        Args:
            monkeypatch: Makes ``fontTools.agl`` unimportable for this test.

        """
        monkeypatch.setitem(sys.modules, "fontTools.agl", None)
        result = MarkItDownParser().parse(
            pua_pdf(_PUA_TEXT, _PUA_CMAP), filename="book.pdf"
        )
        assert result.ok
        assert len(result.pages) == 1
        assert "fa\ue053" in result.pages[0].text

    @staticmethod
    def test_parse_path_spells_pua_glyphs(tmp_path: Path) -> None:
        """Test that the streaming path replaces PUA glyphs like the bytes path.

        **Why this test is important:**
          - Large books go through ``parse_path``; a lane that skipped the table would
            index the ligatures only for small files.

        **What it tests:**
          - A PUA PDF on disk parses to Markdown and pages reading "fact a".

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "book.pdf"
        _ = pdf.write_bytes(pua_pdf(_PUA_TEXT, _PUA_CMAP))
        result = MarkItDownParser().parse_path(str(pdf), filename="book.pdf")
        assert result.ok
        _assert_glyphs_spelled(result.markdown_content)
        _assert_glyphs_spelled(result.pages[0].text)


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

    @staticmethod
    def test_parse_pages_spells_pua_glyphs(tmp_path: Path) -> None:
        """Test that a page range replaces PUA glyphs with their names' text.

        **Why this test is important:**
          - A long book is parsed in page ranges; each range must spell its ligatures.

        **What it tests:**
          - Range 1-1 of a PUA PDF yields Markdown and a page reading "fact a".

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "book.pdf"
        _ = pdf.write_bytes(pua_pdf(_PUA_TEXT, _PUA_CMAP))
        result = MarkItDownParser().parse_pages(
            str(pdf), first_page=1, last_page=1, filename="book.pdf"
        )
        assert result.ok
        _assert_glyphs_spelled(result.markdown_content)
        _assert_glyphs_spelled(result.pages[0].text)

    @staticmethod
    def test_render_glyphs_draws_each_drawn_point_once(tmp_path: Path) -> None:
        """Test that each requested PUA glyph the document draws renders to one PNG.

        **Why this test is important:**
          - OCR of a glyph no name spells needs that glyph's image alone; a point the
            document never draws must not produce an image (or an error).

        **What it tests:**
          - The ligature point renders to PNG bytes; a point absent from the document
            is left out of the result.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = tmp_path / "book.pdf"
        _ = pdf.write_bytes(pua_pdf(_PUA_TEXT, _PUA_CMAP))
        images = MarkItDownParser.render_glyphs(
            str(pdf), points=[0xE053, 0xE000], dpi=144
        )
        assert list(images) == [0xE053]
        assert images[0xE053].startswith(b"\x89PNG")
