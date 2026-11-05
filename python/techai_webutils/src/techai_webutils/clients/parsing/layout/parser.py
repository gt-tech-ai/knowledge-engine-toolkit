"""``LayoutParser``: PDFs through Docling (typed blocks, reading order, tables, OCR), bounded.

A PDF is converted by Docling inside the same bounded child process as the ``isolated`` parser
(the address-space cap and the wall-clock timeout, both from the parser config), and comes back
as a ``ParsedDocument`` with ``blocks`` and Docling's markdown. Every other format falls back to
the ``isolated`` parser unchanged. ``parse_pages`` converts one page range, so a consumer can
parse a large PDF in parts and checkpoint each.

Docling (the ``layout`` extra) is imported only in the child. Its model weights load from
``artifacts_path`` when set (a directory baked into the image, so a parse downloads nothing);
when empty Docling fetches them on first use. ``ocr`` turns on Docling's OCR for pages and regions
with no text layer. Each conversion (a whole PDF or one page range) is one bounded child, so it
costs ``cost()``: the address-space cap and one CPU. The child is spawned, not forked: Docling's
native libraries (torch) abort in a forked child on macOS, and the forkserver does not preload
Docling, so a fresh interpreter costs about the same.
"""

from __future__ import annotations

import asyncio
import importlib
import os
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.parsing.isolated import (
    SPAWN_CONTEXT,
    IsolatedParser,
    IsolationError,
    detect_format,
    isolation_error_tag,
)
from techai_webutils.clients.parsing.layout.mapping import parsed_from_docling
from techai_webutils.core.domain import DocumentFormat, ParsedDocument

if TYPE_CHECKING:
    from collections.abc import Callable
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


def convert_with_docling(path: str, options: LayoutOptions) -> DoclingDocument:
    """Convert the PDF at ``path`` with Docling (runs in the parse child; imports Docling there)."""
    converter_module = importlib.import_module("docling.document_converter")
    base_models = importlib.import_module("docling.datamodel.base_models")
    pipeline_options = importlib.import_module("docling.datamodel.pipeline_options")
    pdf_options = pipeline_options.PdfPipelineOptions(
        do_ocr=options.ocr,
        do_table_structure=True,
        artifacts_path=options.artifacts_path or None,
    )
    converter = converter_module.DocumentConverter(
        format_options={
            base_models.InputFormat.PDF: converter_module.PdfFormatOption(
                pipeline_options=pdf_options
            )
        }
    )
    result = converter.convert(path, page_range=(options.first_page, options.last_page))
    return cast("DoclingDocument", result.document)


def _write_scratch_pdf(content: bytes) -> str:
    """Write ``content`` to a new temporary ``.pdf`` file and return its path."""
    fd, scratch = tempfile.mkstemp(suffix=".pdf")
    with os.fdopen(fd, "wb") as handle:
        handle.write(content)
    return scratch


def _parse_layout(
    path: str,
    options: LayoutOptions,
    convert: Callable[[str, LayoutOptions], DoclingDocument],
) -> ParsedDocument:
    """Child entry: convert the PDF and map it to a ``ParsedDocument`` (module-level: picklable)."""
    return parsed_from_docling(convert(path, options))


class LayoutParser(IsolatedParser):
    """Parses PDFs with Docling in a bounded child; other formats with the ``isolated`` parser."""

    _context = SPAWN_CONTEXT
    """Docling runs in a spawned child (see the module docs)."""

    def __init__(
        self,
        *,
        memory_bytes: int,
        timeout_seconds: float,
        executor: Executor | None = None,
        artifacts_path: str = "",
        ocr: bool = True,
        convert: Callable[[str, LayoutOptions], DoclingDocument] = convert_with_docling,
    ) -> None:
        """Bound each parse like ``IsolatedParser``; ``convert`` runs in the child (picklable)."""
        super().__init__(
            memory_bytes=memory_bytes, timeout_seconds=timeout_seconds, executor=executor
        )
        self._options = LayoutOptions(artifacts_path=artifacts_path, ocr=ocr)
        self._convert = convert

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content``: a PDF through Docling (via a scratch file), else ``isolated``."""
        if detect_format(declared, filename, content) != DocumentFormat.PDF:
            return await super().parse(content, declared=declared, filename=filename)
        scratch = await asyncio.to_thread(_write_scratch_pdf, content)
        try:
            return await self._layout(scratch, self._options, declared, filename)
        finally:
            await asyncio.to_thread(Path(scratch).unlink, missing_ok=True)

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse the file at ``path``: a PDF through Docling, else ``isolated``."""
        if detect_format(declared, filename or path, b"") != DocumentFormat.PDF:
            return await super().parse_path(path, declared=declared, filename=filename)
        return await self._layout(path, self._options, declared, filename)

    async def parse_pages(
        self, path: str, *, first_page: int, last_page: int, filename: str = ""
    ) -> ParsedDocument:
        """Parse pages ``first_page``..``last_page`` (1-based, inclusive) of the PDF at ``path``."""
        options = LayoutOptions(
            self._options.artifacts_path, self._options.ocr, first_page, last_page
        )
        return await self._layout(path, options, "application/pdf", filename)

    async def _layout(
        self, path: str, options: LayoutOptions, declared: str, filename: str
    ) -> ParsedDocument:
        """Run the Docling conversion in the bounded child; a failure becomes a failed document."""
        try:
            return await self._isolated(_parse_layout, path, options, self._convert)
        except TimeoutError:
            return self._failed_path(declared, filename or path, "parse_timeout")
        except IsolationError as exc:
            return self._failed_path(declared, filename or path, isolation_error_tag(exc))
