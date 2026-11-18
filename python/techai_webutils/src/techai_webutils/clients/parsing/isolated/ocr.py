"""OCR for text-less PDF pages: render the page, ask a vision model, fill the text.

A scanned page has no text layer, so a text parse leaves it blank. ``PageOcr`` finds those
pages in a parsed range and reads each through a ``VisionClient``, one call at a time (a
document's pages are read in order, never fanned out, so the model's rate limit is never
the parser's concern). The page render happens in the bounded parse child, supplied as a
``render`` callable; this module holds only the policy, so it imports neither pymupdf nor
a model SDK.
"""

from __future__ import annotations

import dataclasses
import re
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.domain import Page, ParsedDocument
from techai_webutils.core.errors import AppError, AppValueError

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.interfaces.vision import VisionClient

_WORD_RE = re.compile(r"\S+")
"""Splits text into words for the document's word count."""
_DEFAULT_DPI = 150
"""Render resolution: sharp enough for body text, small enough for a model's image cap."""


class OcrKind(StrEnum):
    """Which page-OCR backend to build."""

    NONE = "none"
    """No OCR: a text-less page stays blank (the default)."""
    BEDROCK = "bedrock"
    """Read text-less pages through an injected ``VisionClient`` on a Bedrock model."""


@dataclass(frozen=True, slots=True)
class OcrConfig:
    """Selects and configures page OCR."""

    kind: OcrKind = OcrKind.NONE
    """Which backend to build (``none`` by default)."""
    dpi: int = _DEFAULT_DPI
    """The resolution pages are rendered at before the model reads them."""
    model_id: str = ""
    """The vision model; required by ``bedrock``."""

    def __post_init__(self) -> None:
        """Refuse a render resolution below 1 at construction, not on the first page.

        Raises:
            AppValueError: ``dpi`` is below 1 (``INVALID_INPUT``).

        """
        if self.dpi < 1:
            msg = f"ocr dpi must be at least 1, got {self.dpi}"
            raise AppValueError(msg)


class PageOcr:
    """Fills the blank pages of a parsed document by reading them with a vision model."""

    def __init__(self, client: VisionClient, *, model_id: str, dpi: int) -> None:
        """Bind the vision client, its model and the render resolution.

        Args:
            client: Reads one page image.
            model_id: The vision model ``client`` is asked for.
            dpi: The resolution pages are rendered at.

        """
        self._client: VisionClient = client
        self._model_id: str = model_id
        self._dpi: int = dpi

    @property
    def dpi(self) -> int:
        """The resolution the caller renders pages at."""
        return self._dpi

    async def fill(
        self, document: ParsedDocument, render: Callable[[int], Awaitable[bytes]]
    ) -> ParsedDocument:
        """Read every blank page of ``document`` and return it with their text filled in.

        Pages are read sequentially in page order, one model call each. A page the model
        cannot read stops the run: the document comes back with ``error`` naming the page.
        The markdown is always the page join (each non-blank page's stripped text,
        separated by a blank line), so a whole-document parse and a page-range parse come
        back in the same shape whether or not a page was blank.

        Args:
            document: A parsed document or page range.
            render: Renders one page (by its number) to PNG bytes at ``dpi``.

        Returns:
            ``document`` with the blank pages' text, markdown and word count refreshed;
            ``document`` itself when no page is blank and its markdown is the page join.

        """
        pages: list[Page] = []
        for page in document.pages:
            if page.text.strip():
                pages.append(page)
                continue
            try:
                text = await self._client.describe_page(
                    await render(page.page_number), model_id=self._model_id
                )
            except AppError as exc:
                return dataclasses.replace(
                    document, error=f"ocr_failed: page {page.page_number}: {exc.code}"
                )
            pages.append(Page(page_number=page.page_number, text=text))
        markdown = "\n\n".join(p.text.strip() for p in pages if p.text.strip())
        if pages == document.pages and markdown == document.markdown_content:
            return document
        return dataclasses.replace(
            document,
            pages=pages,
            markdown_content=markdown,
            word_count=len(_WORD_RE.findall(markdown)),
        )


def ocr_from_config(config: OcrConfig, *, client: VisionClient | None) -> PageOcr | None:
    """Build the page OCR ``config.kind`` names; ``None`` for ``none``.

    Args:
        config: Selects the backend and carries its settings.
        client: The vision client ``bedrock`` reads pages through.

    Returns:
        The page OCR, or ``None`` when ``config.kind`` is ``none``.

    Raises:
        AppValueError: An unknown kind, or ``bedrock`` with no model id or no client
            (``INVALID_INPUT``).

    """
    if config.kind is OcrKind.NONE:
        return None
    if config.kind is OcrKind.BEDROCK:
        if not config.model_id or client is None:
            msg = "ocr kind 'bedrock' needs a model_id and a vision client"
            raise AppValueError(msg)
        return PageOcr(client, model_id=config.model_id, dpi=config.dpi)
    msg = f"unknown ocr kind: {config.kind!r}"
    raise AppValueError(msg)
