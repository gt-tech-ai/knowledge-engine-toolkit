"""Env-aware document-splitter factory (foundation/logger ``NewFromConfig`` pattern).

Selects the ``split`` backend from ``SplitterConfig.kind``: ``kb_unit`` (page/paragraph
pack to a byte ceiling, the default), ``code`` (one unit per top-level declaration, from
a tree-sitter parse) and ``markdown`` (one unit per ``##`` section). Unknown kinds fail
loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.splitting.code import CodeSplitter as _LazyCodeSplitter
    from techai_webutils.clients.splitting.kb_unit import (
        KbUnitSplitter as _LazyKbUnitSplitter,
    )
    from techai_webutils.clients.splitting.markdown import (
        MarkdownSplitter as _LazyMarkdownSplitter,
    )
    from techai_webutils.core.interfaces.splitter import DocumentSplitter

# Default per-index-unit byte ceiling (25 MiB); the composition root overrides it from
# config.
_DEFAULT_MAX_BYTES = 25 << 20
"""Default per-index-unit byte ceiling (25 MiB) for the ``kb_unit`` backend; overridden
from config."""

_DEFAULT_SYMBOL_MAX_BYTES = 8192
"""Default size a ``code`` declaration unit may reach before it splits at its nested
blocks (8 KiB, about two thousand tokens)."""

_CODE_EXTRA_MESSAGE = (
    "the code splitter needs the [code] extra: install techai-webutils[code]"
)
"""The error a ``code`` kind raises when tree-sitter is not installed."""


class SplitterKind(StrEnum):
    """Which document-splitter backend to build."""

    KB_UNIT = "kb_unit"
    """Pack contiguous pages/paragraphs into KB-sized index units (the default)."""

    CODE = "code"
    """One unit per top-level declaration of a Go, Python or TypeScript file; other files
    fall back to ``kb_unit``. Needs the ``code`` extra."""

    MARKDOWN = "markdown"
    """One unit per ``##`` section, each prefixed with the document's title, with the
    YAML frontmatter stripped and returned on every unit."""


@dataclass(frozen=True, slots=True)
class SplitterConfig:
    """Document-splitter configuration.

    The consumer maps its own config section onto it.
    """

    kind: SplitterKind = SplitterKind.KB_UNIT
    """Selects the split backend."""
    max_bytes: int = _DEFAULT_MAX_BYTES
    """Per-index-unit byte ceiling a large parse is split up to (a huge value is a
    pass-through); the ``code`` kind's fallback packs to it too."""
    symbol_max_bytes: int = _DEFAULT_SYMBOL_MAX_BYTES
    """The ``code`` kind's budget: a declaration larger than this splits at its nested
    blocks."""


def splitter_from_config(config: SplitterConfig) -> DocumentSplitter:
    """Build the ``DocumentSplitter`` selected by ``config.kind``.

    Each backend is imported only when selected, so the ``code`` kind's tree-sitter
    dependency stays optional.

    Args:
        config: Selects the backend and carries its settings.

    Returns:
        The ``DocumentSplitter`` selected by ``config.kind``.

    Raises:
        AppValueError: Unknown splitter kind, or the ``code`` kind without its extra.

    """
    if config.kind == SplitterKind.KB_UNIT:
        kb_unit_cls = cast(
            "type[_LazyKbUnitSplitter]",
            importlib.import_module(
                "techai_webutils.clients.splitting.kb_unit"
            ).KbUnitSplitter,
        )
        return kb_unit_cls(max_bytes=config.max_bytes)
    if config.kind == SplitterKind.CODE:
        try:
            module = importlib.import_module("techai_webutils.clients.splitting.code")
        except ImportError as exc:
            raise AppValueError(_CODE_EXTRA_MESSAGE) from exc
        code_cls = cast("type[_LazyCodeSplitter]", module.CodeSplitter)
        return code_cls(
            max_bytes=config.max_bytes, symbol_max_bytes=config.symbol_max_bytes
        )
    if config.kind == SplitterKind.MARKDOWN:
        markdown_cls = cast(
            "type[_LazyMarkdownSplitter]",
            importlib.import_module(
                "techai_webutils.clients.splitting.markdown"
            ).MarkdownSplitter,
        )
        return markdown_cls()
    msg = f"unknown splitter kind: {config.kind!r}"
    raise AppValueError(msg)
