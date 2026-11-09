"""DocumentParser stage contract: parse document bytes.

(or a file path) into a ``ParsedDocument``.

The shared, dependency-free parse seam every deployable programs against (mirrors Go's
``go/core/interfaces``). The config-selected backends in
``techai_webutils.clients.parsing`` satisfy it; a parser never raises for a bad document —
it returns a ``ParsedDocument`` carrying the error. ``CostedParser``, ``PagedParser`` and
``TextLayerParser`` are optional capabilities a backend may add.

Imported by full submodule path (``techai_webutils.core.interfaces.parser``), NOT
re-exported from ``core.interfaces`` — the package already exports a legacy, unrelated
``DocumentParser`` (see ``core.interfaces.document``); re-exporting this one would shadow
it.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import ParsedDocument


@dataclass(frozen=True, slots=True)
class ParseCost:
    """What parsing one document costs the process: peak memory and CPUs."""

    memory_bytes: int
    """Peak memory one parse may use (the isolated parser's per-parse cap)."""
    cpu: float
    """CPUs one parse keeps busy."""


@runtime_checkable
class CostedParser(Protocol):
    """A parser that declares its per-document cost, so lanes and Ray tasks size from it.

    A capability beside ``DocumentParser`` (a Protocol, so a default method would not
    reach structural implementers): a consumer checks ``isinstance(parser, CostedParser)``
    and falls back to its lane's configured parser memory when a parser does not implement
    it.
    """

    def cost(self) -> ParseCost:
        """Return the cost of parsing one document."""
        ...


@runtime_checkable
class PagedParser(Protocol):
    """A parser that can count a document's pages and parse one page range of it.

    A capability beside ``DocumentParser`` (like ``CostedParser``): a consumer that parses
    a large PDF in parts, checkpointing each, checks ``isinstance(parser, PagedParser)``
    and parses the document whole when a parser does not implement it. ``join_parsed``
    (``core.domain``) joins the parts back into one document.
    """

    async def page_count(self, path: str) -> int:
        """Return the number of pages of the PDF at ``path``.

        0 when it is not a readable PDF.
        """
        ...

    async def parse_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` (1-based.

        Inclusive) of the PDF at ``path``.
        """
        ...


@dataclass(frozen=True, slots=True)
class PageText:
    """How much readable text one PDF page's text layer holds."""

    page: int
    """The 1-based page."""
    chars: int
    """Non-whitespace characters in the page's text layer (0: no text layer, a scanned
    page)."""
    unreadable: int
    """Of those, characters no reader renders: the replacement character, control,
    private-use, surrogate or unassigned code points (a broken font encoding produces
    them)."""

    @property
    def unreadable_share(self) -> float:
        """The share of the page's characters that are unreadable (0.

        For an empty page).
        """
        return self.unreadable / self.chars if self.chars else 0.0


@runtime_checkable
class TextLayerParser(Protocol):
    """A parser that can read each PDF page's text layer and OCR a page range regardless.

    Of it.

    A capability beside ``PagedParser``: a consumer reads ``text_layers`` to find pages
    with no text (OCR'd anyway by a parser that OCRs) or a garbled one, and parses a range
    holding a garbled page with ``ocr_pages``, which replaces the text layer with
    full-page OCR.
    """

    async def text_layers(self, path: str) -> tuple[PageText, ...]:
        """Return each page's text-layer counts for the PDF at ``path``.

        Empty when not a readable PDF.
        """
        ...

    async def ocr_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` with full-page OCR.

        Ignoring the text layer.
        """
        ...


@runtime_checkable
class DocumentParser(Protocol):
    """Parses raw document bytes or a file path into a ``ParsedDocument``.

    (never raises for a bad doc).
    """

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content`` given the declared type + filename.

        (errors carried in the result).
        """
        ...

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path``.

        (bounded memory — the large-document lane).
        """
        ...
