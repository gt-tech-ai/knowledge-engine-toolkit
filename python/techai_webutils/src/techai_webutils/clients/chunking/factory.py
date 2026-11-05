"""Env-aware chunker factory (foundation/logger ``NewFromConfig`` pattern).

Selects the embedding-granularity chunk backend from ``ChunkerConfig.kind``: ``fixed`` (the
default; deterministic paragraph-pack to a character budget) or ``sentence`` (token-sized chunks
at sentence boundaries, tables and code kept whole; a ``StructuredChunker``). Unknown kinds fail
loudly, matching the Go ``NewFromConfig`` contract.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.clients.chunking.sentence.chunker import SentenceChunker
    from techai_webutils.core.interfaces.chunker import Chunker
    from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter

# Mirrors ``FixedChunker``'s default (2048 chars ≈ 512 tokens; the prod Bedrock KB granularity). Kept
# as a config default here so building ``ChunkerConfig`` needs no eager backend import.
_DEFAULT_MAX_CHARS = 2048
"""Default per-chunk character budget for the fixed backend (mirrors ``FixedChunker._DEFAULT_MAX_CHARS``)."""


class ChunkerKind(StrEnum):
    """Which embedding-granularity chunker backend to build."""

    FIXED = "fixed"
    """Deterministic paragraph-pack chunking to a fixed character budget (the default)."""
    SENTENCE = "sentence"
    """Token-sized chunks at sentence boundaries, tables and code kept whole (``SentenceChunker``)."""


@dataclass(frozen=True, slots=True)
class ChunkerConfig:
    """Chunker configuration (the consumer maps its own config section onto it)."""

    kind: ChunkerKind = ChunkerKind.FIXED
    """Selects the chunk backend (``fixed`` by default)."""
    max_chars: int = _DEFAULT_MAX_CHARS
    """Per-chunk character budget for the ``fixed`` backend."""
    max_tokens: int = 512
    """``sentence``: the most tokens a chunk holds, as the injected tokenizer counts them."""
    overlap_tokens: int = 0
    """``sentence``: tokens of a paragraph chunk's last sentences repeated at the next chunk's
    start (below ``max_tokens``; 0, the default, repeats nothing)."""
    window_chars: int = 100_000
    """``sentence``: the most characters passed to the sentence splitter in one call."""


def chunker_from_config(
    config: ChunkerConfig,
    *,
    splitter: SentenceSplitter | None = None,
    tokenizer: Callable[[str], int] | None = None,
) -> Chunker:
    """Build the ``Chunker`` selected by ``config.kind``. Unknown kinds fail loudly.

    ``sentence`` needs ``splitter`` (a ``SentenceSplitter``) and ``tokenizer`` (the embedding
    model's token count for a text); ``fixed`` ignores both.

    Raises:
        AppValueError: An unknown kind, ``sentence`` without a splitter or tokenizer, or bad
            ``sentence`` limits (``INVALID_INPUT``).

    """
    if config.kind is ChunkerKind.FIXED:
        from techai_webutils.clients.chunking.fixed import FixedChunker  # noqa: PLC0415 — lazy backend

        return FixedChunker(max_chars=config.max_chars)
    if config.kind is ChunkerKind.SENTENCE:
        if splitter is None or tokenizer is None:
            msg = "the sentence chunker needs a sentence splitter and a tokenizer"
            raise AppValueError(msg)
        module = importlib.import_module(
            "techai_webutils.clients.chunking.sentence.chunker"
        )
        chunker = cast("type[SentenceChunker]", module.SentenceChunker)
        return chunker(
            splitter,
            tokenizer,
            max_tokens=config.max_tokens,
            overlap_tokens=config.overlap_tokens,
            window_chars=config.window_chars,
        )
    msg = f"unknown chunker kind: {config.kind!r}"
    raise AppValueError(msg)
