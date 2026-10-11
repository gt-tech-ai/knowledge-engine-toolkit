"""Env-aware document-splitter factory (foundation/logger ``NewFromConfig`` pattern).

Selects the ``split`` backend from ``SplitterConfig.kind``. Only ``kb_unit`` (page/paragraph pack to a
byte ceiling) ships today; table-atomic / layout-aware splitters are future ``Kind``s.
Unknown kinds fail loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.splitter import DocumentSplitter

# Default per-index-unit byte ceiling (25 MiB); the composition root overrides it from config.
_DEFAULT_MAX_BYTES = 25 << 20
"""Default per-index-unit byte ceiling (25 MiB) for the ``kb_unit`` backend; overridden from config."""


class SplitterKind(StrEnum):
    """Which document-splitter backend to build."""

    KB_UNIT = "kb_unit"
    """Pack contiguous pages/paragraphs into KB-sized index units (the only backend today)."""


@dataclass(frozen=True, slots=True)
class SplitterConfig:
    """Document-splitter configuration (the consumer maps its own config section onto it)."""

    kind: SplitterKind = SplitterKind.KB_UNIT
    """Selects the split backend (only ``kb_unit`` ships today)."""
    max_bytes: int = _DEFAULT_MAX_BYTES
    """Per-index-unit byte ceiling a large parse is split up to (a huge value is a pass-through)."""


def splitter_from_config(config: SplitterConfig) -> DocumentSplitter:
    """Build the ``DocumentSplitter`` selected by ``config.kind`` (kb_unit). Unknown kinds fail loudly."""
    if config.kind is SplitterKind.KB_UNIT:
        from techai_webutils.clients.splitting.kb_unit import KbUnitSplitter  # noqa: PLC0415 — lazy backend

        return KbUnitSplitter(max_bytes=config.max_bytes)
    msg = f"unknown splitter kind: {config.kind!r}"
    raise AppValueError(msg)
