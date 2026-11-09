"""Env-aware document-parser factory (foundation/logger ``NewFromConfig`` pattern).

Selects the parse backend from ``ParserConfig.kind``: ``isolated`` (the default;
MarkItDown + pymupdf in a bounded subprocess) or ``layout`` (PDFs through Docling into
typed blocks, in the same bounded subprocess; other formats through ``isolated``). Unknown
kinds fail loudly, matching the Go ``NewFromConfig`` contract. The heavy deps stay lazy —
each backend module is imported only when its kind is selected, and the parser libraries
load only in the parse child or forkserver.
"""

from __future__ import annotations

import importlib
import importlib.util
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from concurrent.futures import Executor

    from techai_webutils.clients.parsing.layout.parser import (
        LayoutConverter,
        LayoutParser,
    )
    from techai_webutils.core.interfaces.parser import DocumentParser

# Default per-parse address-space cap (512 MiB) and wall-clock ceiling (60 s) for the
# isolated backend; the composition root overrides them from config.
_DEFAULT_MEMORY_BYTES = 512 * 1024 * 1024
"""Default per-parse RLIMIT_AS cap (512 MiB) for the isolated backend."""
_DEFAULT_TIMEOUT_SECONDS = 60.0
"""Default per-parse wall-clock ceiling (seconds) for the isolated backend."""


class ParserKind(StrEnum):
    """Which document-parser backend to build."""

    ISOLATED = "isolated"
    """MarkItDown + pymupdf run in a memory/wall-clock-bounded subprocess (the
    default)."""
    LAYOUT = "layout"
    """PDFs through Docling into typed blocks (bounded the same way); other formats
    ``isolated``."""


@dataclass(frozen=True, slots=True)
class ParserConfig:
    """Document-parser configuration.

    (the consumer maps its own config section onto it).
    """

    kind: ParserKind = ParserKind.ISOLATED
    """Selects the parse backend (``isolated`` by default)."""
    memory_bytes: int = _DEFAULT_MEMORY_BYTES
    """Per-parse address-space (RLIMIT_AS) cap for the parse subprocess (``layout`` needs
    several
    GiB: Docling loads its models in the child)."""
    timeout_seconds: float = _DEFAULT_TIMEOUT_SECONDS
    """Per-parse wall-clock ceiling (seconds) before the parse subprocess is killed."""
    artifacts_path: str = ""
    """``layout``: the directory holding Docling's model weights (empty: Docling downloads
    them)."""
    ocr: bool = True
    """``layout``: run OCR on pages and regions with no text layer."""


def parser_from_config(
    config: ParserConfig,
    *,
    executor: Executor | None = None,
    convert: LayoutConverter | None = None,
) -> DocumentParser:
    """Build the ``DocumentParser`` selected by ``config.kind``.

    Unknown kinds fail loudly.

    ``executor`` is the pool parses wait on, sized by the composition root to the lane's
    concurrency; ``None`` keeps the event loop's default pool. ``convert`` replaces the
    ``layout`` kind's Docling conversion (a module-level, picklable function). Each
    backend module (and its import graph) is loaded lazily here, so a composition root
    never pays for an unselected kind.

    Raises:
        AppValueError: An unknown kind, or ``layout`` with neither Docling installed (the
            ``layout`` extra) nor an injected ``convert`` (``INVALID_INPUT``).

    """
    if config.kind is ParserKind.LAYOUT:
        return _layout(config, executor, convert)
    if config.kind is ParserKind.ISOLATED:
        from techai_webutils.clients.parsing.isolated import IsolatedParser  # noqa: PLC0415 — lazy backend

        return IsolatedParser(
            memory_bytes=config.memory_bytes,
            timeout_seconds=config.timeout_seconds,
            executor=executor,
        )
    msg = f"unknown parser kind: {config.kind!r}"
    raise AppValueError(msg)


def _layout(
    config: ParserConfig,
    executor: Executor | None,
    convert: LayoutConverter | None,
) -> DocumentParser:
    """Build the ``layout`` parser.

    Refusing it when Docling is missing and nothing is injected.
    """
    if convert is None and importlib.util.find_spec("docling") is None:
        msg = "the layout parser needs Docling: install techai-webutils[layout]"
        raise AppValueError(msg)
    module = importlib.import_module("techai_webutils.clients.parsing.layout.parser")
    parser = cast("type[LayoutParser]", module.LayoutParser)
    return parser(
        memory_bytes=config.memory_bytes,
        timeout_seconds=config.timeout_seconds,
        executor=executor,
        artifacts_path=config.artifacts_path,
        ocr=config.ocr,
        convert=convert or cast("LayoutConverter", module.convert_with_docling),
    )
