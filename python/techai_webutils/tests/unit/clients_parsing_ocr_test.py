"""Tests for page OCR: the factory and filling the blank pages of a parsed range."""

import asyncio
from typing import cast
from unittest.mock import AsyncMock, call

import pytest

from techai_webutils.clients.parsing.isolated.ocr import (
    OcrConfig,
    OcrKind,
    PageOcr,
    ocr_from_config,
)
from techai_webutils.core.domain import DocumentFormat, Page, ParsedDocument
from techai_webutils.core.errors import AppValueError, UnavailableError
from techai_webutils.core.interfaces.vision import VisionClient

_MODEL = "vision-model"
_DPI = 150
_FIVE_PAGES = 5
_HIRES_DPI = 200


def _document(blank: set[int]) -> ParsedDocument:
    """Build a 5-page parsed range whose pages in ``blank`` have no text.

    Args:
        blank: The page numbers with no text layer.

    Returns:
        The document.

    """
    pages = [
        Page(page_number=n, text="" if n in blank else f"text{n}")
        for n in range(1, _FIVE_PAGES + 1)
    ]
    return ParsedDocument(
        markdown_content="\n\n".join(p.text for p in pages if p.text),
        document_format=DocumentFormat.PDF,
        total_pages=_FIVE_PAGES,
        pages=pages,
    )


def _vision(*texts: str) -> AsyncMock:
    """Build a mock vision client that answers each call with the next of `texts`.

    Args:
        *texts: The text returned by successive `describe_page` calls.

    Returns:
        The mock.

    """
    client = AsyncMock(spec=VisionClient)
    cast("AsyncMock", client.describe_page).side_effect = list(texts)
    return client


def _calls(client: AsyncMock) -> list[object]:
    """Return the `describe_page` calls the mock awaited, in order.

    Args:
        client: The mock vision client.

    Returns:
        The awaited calls.

    """
    describe = cast("AsyncMock", client.describe_page)
    return cast("list[object]", describe.await_args_list)


async def _render(number: int) -> bytes:
    """Render a page as bytes naming it (stands in for the child's PNG render).

    Args:
        number: The page number.

    Returns:
        The page's "image".

    """
    await asyncio.sleep(0)
    return f"read{number}".encode()


class TestOcrFromConfig:
    """``ocr_from_config`` selects the backend and fails loudly on a bad config."""

    @staticmethod
    def test_none_builds_nothing_and_bedrock_builds_page_ocr() -> None:
        """Test that ``none`` yields no OCR and ``bedrock`` yields a PageOcr.

        **Why this test is important:**
          - The default must leave parsing byte-identical (no OCR), and the selected
            kind must build the object the parser fills pages with.

        **What it tests:**
          - ``none`` returns ``None``; ``bedrock`` with a model and client returns a
            ``PageOcr`` at the configured dpi.
        """
        assert ocr_from_config(OcrConfig(), client=None) is None
        ocr = ocr_from_config(
            OcrConfig(OcrKind.BEDROCK, dpi=_HIRES_DPI, model_id=_MODEL), client=_vision()
        )
        assert isinstance(ocr, PageOcr)
        assert ocr.dpi == _HIRES_DPI

    @staticmethod
    @pytest.mark.parametrize(
        ("config", "client"),
        [
            (OcrConfig(OcrKind.BEDROCK, model_id=""), AsyncMock(spec=VisionClient)),
            (OcrConfig(OcrKind.BEDROCK, model_id=_MODEL), None),
        ],
    )
    def test_bedrock_without_model_or_client_is_rejected(
        config: OcrConfig, client: VisionClient | None
    ) -> None:
        """Test that ``bedrock`` with no model id, or no client, is refused at build.

        **Why this test is important:**
          - A misconfigured OCR must fail at boot, not on the first scanned page.

        **What it tests:**
          - An empty model id, and a missing client, each raise ``AppValueError``.

        Args:
            config: The config to build.
            client: The vision client passed.

        """
        with pytest.raises(AppValueError, match="model_id"):
            _ = ocr_from_config(config, client=client)


class TestPageOcrFill:
    """``PageOcr.fill`` reads exactly the blank pages, in order."""

    @staticmethod
    @pytest.mark.asyncio
    async def test_reads_blank_pages_in_order_and_refreshes_the_document() -> None:
        """Test that pages 2 and 5 are read, in that order, one call each.

        **Why this test is important:**
          - One call per text-less page, sequentially, is the cost and rate-limit
            contract; text pages must not be sent to the model at all.

        **What it tests:**
          - The client is called for page 2 then page 5 with the configured model;
            their text, the markdown and the word count are filled in; page 1/3/4 text
            is untouched.
        """
        client = _vision("read2", "read5")
        ocr = PageOcr(client, model_id=_MODEL, dpi=_DPI)
        result = await ocr.fill(_document({2, 5}), _render)
        assert _calls(client) == [
            call(b"read2", model_id=_MODEL),
            call(b"read5", model_id=_MODEL),
        ]
        assert result.ok
        assert [p.text for p in result.pages] == [
            "text1",
            "read2",
            "text3",
            "text4",
            "read5",
        ]
        assert result.markdown_content == "text1\n\nread2\n\ntext3\n\ntext4\n\nread5"
        assert result.word_count == _FIVE_PAGES

    @staticmethod
    @pytest.mark.asyncio
    async def test_no_blank_page_makes_no_call() -> None:
        """Test that a document with a text layer on every page is returned as is.

        **Why this test is important:**
          - Text PDFs are the common case; OCR must cost them nothing.

        **What it tests:**
          - No model call, and the very same document comes back.
        """
        client = _vision()
        document = _document(set())
        result = await PageOcr(client, model_id=_MODEL, dpi=_DPI).fill(document, _render)
        assert result is document
        assert not _calls(client)

    @staticmethod
    @pytest.mark.asyncio
    async def test_a_failed_page_fails_the_document_naming_the_page() -> None:
        """Test that a model failure on page 5 surfaces as the document's error.

        **Why this test is important:**
          - A silently blank page would index a gap as if it were complete; the failure
            must name the page so the operator can find it.

        **What it tests:**
          - The result is not ok and its error names page 5.
        """
        client = _vision()
        cast("AsyncMock", client.describe_page).side_effect = UnavailableError(
            "model down"
        )
        result = await PageOcr(client, model_id=_MODEL, dpi=_DPI).fill(
            _document({5}), _render
        )
        assert not result.ok
        assert "page 5" in result.error
