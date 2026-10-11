"""Env-aware document-parser factory (foundation/logger ``NewFromConfig`` pattern).

Selects the parse backend from ``ParserConfig.kind``. Only ``isolated`` (MarkItDown + pymupdf in a
bounded subprocess) ships today; layout-aware / OCR backends are future
``Kind``s. Unknown kinds fail loudly, matching the Go ``NewFromConfig`` contract. The heavy
``markitdown``/``pymupdf`` deps stay lazy — the ``isolated`` backend module is imported only when the
``isolated`` kind is selected, and it in turn imports the parser libs lazily inside its parse methods.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.parser import DocumentParser

# Default per-parse address-space cap (512 MiB) and wall-clock ceiling (60 s) for the isolated backend;
# the composition root overrides them from config.
_DEFAULT_MEMORY_BYTES = 512 * 1024 * 1024
"""Default per-parse RLIMIT_AS cap (512 MiB) for the isolated backend."""
_DEFAULT_TIMEOUT_SECONDS = 60.0
"""Default per-parse wall-clock ceiling (seconds) for the isolated backend."""


class ParserKind(StrEnum):
    """Which document-parser backend to build."""

    ISOLATED = "isolated"
    """MarkItDown + pymupdf run in a memory/wall-clock-bounded subprocess (the only backend today)."""


@dataclass(frozen=True, slots=True)
class ParserConfig:
    """Document-parser configuration (the consumer maps its own config section onto it)."""

    kind: ParserKind = ParserKind.ISOLATED
    """Selects the parse backend (only ``isolated`` ships today)."""
    memory_bytes: int = _DEFAULT_MEMORY_BYTES
    """Per-parse address-space (RLIMIT_AS) cap for the isolated subprocess."""
    timeout_seconds: float = _DEFAULT_TIMEOUT_SECONDS
    """Per-parse wall-clock ceiling (seconds) before the isolated subprocess is killed."""


def parser_from_config(config: ParserConfig) -> DocumentParser:
    """Build the ``DocumentParser`` selected by ``config.kind`` (isolated). Unknown kinds fail loudly.

    The ``isolated`` backend module (and, transitively, the markitdown/pymupdf import graph) is loaded
    lazily here, so a composition root that never selects it never pays the import.
    """
    if config.kind is ParserKind.ISOLATED:
        from techai_webutils.clients.parsing.isolated import IsolatedParser  # noqa: PLC0415 — lazy backend

        return IsolatedParser(
            memory_bytes=config.memory_bytes, timeout_seconds=config.timeout_seconds
        )
    msg = f"unknown parser kind: {config.kind!r}"
    raise AppValueError(msg)
