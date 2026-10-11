"""Env-aware chunker factory (foundation/logger ``NewFromConfig`` pattern).

Selects the embedding-granularity chunk backend from ``ChunkerConfig.kind``.
Only ``fixed`` (deterministic paragraph-pack) ships today; ``semantic``/``layout`` are future ``Kind``s.
Unknown kinds fail loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.chunker import Chunker

# Mirrors ``FixedChunker``'s default (2048 chars ≈ 512 tokens; the prod Bedrock KB granularity). Kept
# as a config default here so building ``ChunkerConfig`` needs no eager backend import.
_DEFAULT_MAX_CHARS = 2048
"""Default per-chunk character budget for the fixed backend (mirrors ``FixedChunker._DEFAULT_MAX_CHARS``)."""


class ChunkerKind(StrEnum):
    """Which embedding-granularity chunker backend to build."""

    FIXED = "fixed"
    """Deterministic paragraph-pack chunking to a fixed character budget (the only backend today)."""


@dataclass(frozen=True, slots=True)
class ChunkerConfig:
    """Chunker configuration (the consumer maps its own config section onto it)."""

    kind: ChunkerKind = ChunkerKind.FIXED
    """Selects the chunk backend (only ``fixed`` ships today)."""
    max_chars: int = _DEFAULT_MAX_CHARS
    """Per-chunk character budget for the ``fixed`` backend."""


def chunker_from_config(config: ChunkerConfig) -> Chunker:
    """Build the ``Chunker`` selected by ``config.kind`` (fixed). Unknown kinds fail loudly."""
    if config.kind is ChunkerKind.FIXED:
        from techai_webutils.clients.chunking.fixed import FixedChunker  # noqa: PLC0415 — lazy backend

        return FixedChunker(max_chars=config.max_chars)
    msg = f"unknown chunker kind: {config.kind!r}"
    raise AppValueError(msg)
