"""Sentence splitters (``SentenceSplitterKind``: ``sentencizer``) and their factory."""

from techai_webutils.clients.sentences.builder import (
    SentenceSplitterConfig,
    SentenceSplitterKind,
    sentence_splitter_from_config,
)
from techai_webutils.clients.sentences.rules import (
    TRANSCRIPT_JOIN_PATTERNS,
    RuledSplitter,
)

__all__ = [
    "TRANSCRIPT_JOIN_PATTERNS",
    "RuledSplitter",
    "SentenceSplitterConfig",
    "SentenceSplitterKind",
    "sentence_splitter_from_config",
]
