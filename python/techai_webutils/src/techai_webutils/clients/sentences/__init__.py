"""Sentence splitters (``SentenceSplitterKind``: ``sentencizer``) and their factory."""

from techai_webutils.clients.sentences.builder import (
    SentenceSplitterConfig,
    SentenceSplitterKind,
    sentence_splitter_from_config,
)

__all__ = [
    "SentenceSplitterConfig",
    "SentenceSplitterKind",
    "sentence_splitter_from_config",
]
