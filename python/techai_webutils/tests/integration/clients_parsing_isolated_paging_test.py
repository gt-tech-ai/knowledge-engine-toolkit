"""Integration tests for ``IsolatedParser``'s paging and page OCR in its real child.

Each test forks the bounded parse child and opens a real PDF in it (pymupdf), so they
run with the integration suite, not the unit run. The vision model is the one external
boundary and is a mock of ``VisionClient``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, cast
from unittest.mock import AsyncMock

import pymupdf
import pytest

from techai_webutils.clients.parsing.isolated.isolated import IsolatedParser
from techai_webutils.clients.parsing.isolated.ocr import PageOcr
from techai_webutils.core.interfaces.parser import PagedParser
from techai_webutils.core.interfaces.vision import VisionClient
from tests.pua_pdf import pua_pdf

if TYPE_CHECKING:
    from pathlib import Path

_THREE_PAGES = 3


def _make_paged_pdf(path: Path, texts: list[str]) -> str:
    """Write a PDF with one page per entry of ``texts`` and return its path.

    Args:
        path: The file to write.
        texts: Each page's text.

    Returns:
        The path of the written PDF, as a string.

    """
    doc = pymupdf.open()
    for text in texts:
        _ = doc.new_page().insert_text((72, 72), text)
    doc.save(str(path))
    return str(path)


@pytest.mark.integration
class TestIsolatedPaging:
    """The ``PagedParser`` capability of ``IsolatedParser`` (bounded subprocess)."""

    @staticmethod
    def test_isolated_parser_is_a_paged_parser() -> None:
        """Test that IsolatedParser satisfies the PagedParser contract.

        **Why this test is important:**
          - A consumer selects page-by-page parsing by
            ``isinstance(parser, PagedParser)``;
            the default parser must qualify so large PDFs are parsed in parts.

        **What it tests:**
          - ``isinstance(IsolatedParser(), PagedParser)`` is true.
        """
        assert isinstance(IsolatedParser(), PagedParser)

    @staticmethod
    @pytest.mark.asyncio
    async def test_page_count_reads_the_page_count_in_a_subprocess(
        tmp_path: Path,
    ) -> None:
        """Test that page_count counts a PDF's pages and is 0 for a non-PDF.

        **Why this test is important:**
          - The count decides whether a document is parsed whole or in parts, and the
            untrusted file must be opened in the bounded child, not the parent.

        **What it tests:**
          - A 3-page PDF counts 3; a ``.txt`` file counts 0.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = _make_paged_pdf(tmp_path / "doc.pdf", ["Alpha", "Bravo", "Charlie"])
        txt = tmp_path / "note.txt"
        _ = txt.write_text("plain")
        parser = IsolatedParser(timeout_seconds=60)
        assert await parser.page_count(pdf) == _THREE_PAGES
        assert await parser.page_count(str(txt)) == 0

    @staticmethod
    @pytest.mark.asyncio
    async def test_parse_pages_returns_only_the_requested_pages(
        tmp_path: Path,
    ) -> None:
        """Test that parse_pages parses just first..last, numbered by position.

        **Why this test is important:**
          - Parsing a large PDF in parts bounds memory per part; each part must carry
            its true page numbers so units and citations point at the right pages.

        **What it tests:**
          - Pages 2-3 of a 3-page PDF come back as pages [2, 3] with no page 1 text.
          - An out-of-range request becomes a failed document, not a raise.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = _make_paged_pdf(tmp_path / "doc.pdf", ["Alpha", "Bravo", "Charlie"])
        parser = IsolatedParser(timeout_seconds=60)
        part = await parser.parse_pages(pdf, first_page=2, last_page=3)
        assert part.ok
        assert [page.page_number for page in part.pages] == [2, 3]
        assert "Bravo" in part.markdown_content
        assert "Alpha" not in part.markdown_content
        bad = await parser.parse_pages(pdf, first_page=2, last_page=9)
        assert not bad.ok

    @staticmethod
    @pytest.mark.asyncio
    async def test_page_count_reads_an_extensionless_pdf_by_its_bytes(
        tmp_path: Path,
    ) -> None:
        """Test that page_count counts a PDF stored without a ``.pdf`` name.

        **Why this test is important:**
          - Uploads are often staged under an id with no extension; detecting the format
            from the name alone counted such a PDF as 0 pages, so it was parsed whole
            instead of in parts.

        **What it tests:**
          - A 3-page PDF written to a path with no extension counts 3.

        Args:
            tmp_path: A fresh directory for the test.

        """
        pdf = _make_paged_pdf(tmp_path / "upload", ["Alpha", "Bravo", "Charlie"])
        parser = IsolatedParser(timeout_seconds=60)
        assert await parser.page_count(pdf) == _THREE_PAGES


_PNG_MAGIC = b"\x89PNG"
_OCR_DPI = 72


@pytest.mark.integration
class TestIsolatedOcr:
    """``IsolatedParser(ocr=…)`` reads text-less pages through a vision client."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_parse_pages_reads_only_the_text_less_page(tmp_path: Path) -> None:
        """Test that a blank page in the range is rendered and read; text pages are not.

        **Why this test is important:**
          - A scanned page has no text layer; without OCR it indexes as a silent gap.
            The render must run in the bounded child and send a real PNG to the model.

        **What it tests:**
          - Pages 1-3 with a blank page 2: one vision call, with PNG bytes; page 2 now
            carries the model's text, pages 1 and 3 keep theirs, and the markdown
            holds all three.
          - Without ``ocr`` the same range leaves page 2 blank (default unchanged).

        Args:
            tmp_path: pytest-provided temporary directory.

        """
        pdf = _make_paged_pdf(tmp_path / "scan.pdf", ["Alpha", "", "Charlie"])
        client = AsyncMock(spec=VisionClient)
        describe = cast("AsyncMock", client.describe_page)
        describe.side_effect = ["Bravo scanned"]
        ocr = PageOcr(client, model_id="m", dpi=_OCR_DPI)
        parser = IsolatedParser(timeout_seconds=60, ocr=ocr)
        part = await parser.parse_pages(pdf, first_page=1, last_page=3)
        assert part.ok
        assert [page.text.strip() for page in part.pages] == [
            "Alpha",
            "Bravo scanned",
            "Charlie",
        ]
        assert "Bravo scanned" in part.markdown_content
        calls = cast(
            "list[tuple[tuple[bytes], dict[str, str]]]", describe.await_args_list
        )
        assert len(calls) == 1
        assert calls[0][0][0].startswith(_PNG_MAGIC)
        assert calls[0][1] == {"model_id": "m"}
        plain = await IsolatedParser(timeout_seconds=60).parse_pages(
            pdf, first_page=1, last_page=3
        )
        assert not plain.pages[1].text.strip()

    @staticmethod
    @pytest.mark.asyncio
    async def test_parse_path_reads_the_text_less_page(tmp_path: Path) -> None:
        """Test that a whole-file parse by path also reads its blank page.

        **Why this test is important:**
          - A short scanned PDF is parsed whole, not in parts; OCR wired only to
            ``parse_pages`` would leave that document's scanned page blank.

        **What it tests:**
          - ``parse_path`` on pages Alpha / blank / Charlie makes one vision call and
            returns the model's text for page 2, also in the markdown.

        Args:
            tmp_path: pytest-provided temporary directory.

        """
        pdf = _make_paged_pdf(tmp_path / "scan.pdf", ["Alpha", "", "Charlie"])
        client = AsyncMock(spec=VisionClient)
        describe = cast("AsyncMock", client.describe_page)
        describe.side_effect = ["Bravo scanned"]
        parser = IsolatedParser(
            timeout_seconds=60, ocr=PageOcr(client, model_id="m", dpi=_OCR_DPI)
        )
        document = await parser.parse_path(pdf, filename="scan.pdf")
        assert document.ok
        assert [page.text.strip() for page in document.pages] == [
            "Alpha",
            "Bravo scanned",
            "Charlie",
        ]
        assert "Bravo scanned" in document.markdown_content
        describe.assert_awaited_once()

    @staticmethod
    @pytest.mark.asyncio
    async def test_parse_path_reads_a_pua_glyph_no_name_spells(tmp_path: Path) -> None:
        """Test that a PUA glyph whose name spells nothing is read through OCR.

        **Why this test is important:**
          - Some embedded fonts name their ligatures ``uniE053`` or not at all; the
            glyph's image is then the only record of its letters. The render must run
            in the bounded child and hand the model a real PNG.

        **What it tests:**
          - A page "fa" + an unnamed PUA glyph: one vision call with PNG bytes, whose
            answer "ct" makes the page and markdown read "fact".

        Args:
            tmp_path: pytest-provided temporary directory.

        """
        pdf = tmp_path / "book.pdf"
        _ = pdf.write_bytes(
            pua_pdf("fa\ue053", {0x61: "a", 0x66: "f", 0xE053: "uniE053"})
        )
        client = AsyncMock(spec=VisionClient)
        describe = cast("AsyncMock", client.describe_page)
        describe.side_effect = ["ct"]
        parser = IsolatedParser(
            timeout_seconds=60, ocr=PageOcr(client, model_id="m", dpi=_OCR_DPI)
        )
        document = await parser.parse_path(str(pdf), filename="book.pdf")
        assert document.ok
        assert "fact" in document.pages[0].text
        assert "fact" in document.markdown_content
        image = cast("bytes", describe.await_args_list[0][0][0])
        assert image.startswith(_PNG_MAGIC)
        describe.assert_awaited_once()
