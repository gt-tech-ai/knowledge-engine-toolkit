"""``contextualizer_from_config``: the config-selected ``ChunkContextualizer``.

``none`` (the default) changes nothing, so indexing costs what it does today. ``header`` is free
and deterministic. ``llm`` calls the injected model once per chunk; the consumer enables it only
with a budget.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.chunking.context.contextualizers import (
    HeaderContextualizer,
    LlmContextualizer,
    NoContextualizer,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.chunk_contextualizer import ChunkContextualizer
    from techai_webutils.core.interfaces.llm import LLMProvider


class ContextualizerKind(StrEnum):
    """Which chunk contextualizer to build."""

    NONE = "none"
    """The chunk text unchanged (the default)."""
    HEADER = "header"
    """The document name, section path and table caption first."""
    LLM = "llm"
    """A model-written context first (one model call per chunk)."""


@dataclass(frozen=True, slots=True)
class ContextualizerConfig:
    """Selects the chunk contextualizer and bounds the ``llm`` kind's calls."""

    kind: ContextualizerKind = ContextualizerKind.NONE
    """The contextualizer to build; ``none`` by default."""
    max_document_chars: int = 32_000
    """``llm``: the document characters sent with each chunk."""
    max_context_tokens: int = 100
    """``llm``: the most tokens the model writes per chunk."""
    concurrency: int = 4
    """``llm``: the most model calls in flight."""


def contextualizer_from_config(
    config: ContextualizerConfig, *, llm: LLMProvider | None = None
) -> ChunkContextualizer:
    """Build the ``ChunkContextualizer`` ``config.kind`` selects; ``llm`` is the ``llm`` kind's model.

    Raises:
        AppValueError: An unknown kind, or ``llm`` without a model or with a limit below 1
            (``INVALID_INPUT``).

    """
    if config.kind == ContextualizerKind.NONE:
        return NoContextualizer()
    if config.kind == ContextualizerKind.HEADER:
        return HeaderContextualizer()
    if config.kind == ContextualizerKind.LLM:
        limits = (
            config.max_document_chars,
            config.max_context_tokens,
            config.concurrency,
        )
        if llm is None or min(limits) < 1:
            msg = "the llm contextualizer needs a model and limits of at least 1"
            raise AppValueError(msg)
        return LlmContextualizer(
            llm,
            max_document_chars=config.max_document_chars,
            max_context_tokens=config.max_context_tokens,
            concurrency=config.concurrency,
        )
    msg = f"unknown contextualizer kind: {config.kind!r}"
    raise AppValueError(msg)
