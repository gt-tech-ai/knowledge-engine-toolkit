"""``sentence_splitter_from_config``: the config-selected ``SentenceSplitter``.

``sentencizer`` (the default and only kind) is spaCy's rule-based sentencizer on a blank
pipeline; spaCy (the ``nlp`` extra) is imported only when a splitter is built.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.sentences.rules import RuledSplitter
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.sentences.sentencizer.splitter import SentencizerSplitter
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter


class SentenceSplitterKind(StrEnum):
    """Which sentence splitter to build."""

    SENTENCIZER = "sentencizer"
    """spaCy's rule-based sentencizer on a blank pipeline per language (the default)."""


@dataclass(frozen=True, slots=True)
class SentenceSplitterConfig:
    """Selects the sentence splitter and its rules."""

    kind: SentenceSplitterKind = SentenceSplitterKind.SENTENCIZER
    """The splitter to build; ``sentencizer`` by default."""
    default_language: str = "en"
    """The rules used when ``split`` names no language (an ISO 639-1 code, or ``xx``)."""
    max_chars: int = 1_000_000
    """The longest text one ``split`` accepts (spaCy's own default limit)."""
    join_patterns: tuple[str, ...] = ()
    """Regular expressions; a sentence fully matching one is joined onto the next
    (``TRANSCRIPT_JOIN_PATTERNS`` for transcripts). Empty by default: no rules, no
    wrapper."""


def sentence_splitter_from_config(
    config: SentenceSplitterConfig, *, metrics: MetricsProvider | None = None
) -> SentenceSplitter:
    """Build the ``SentenceSplitter`` ``config.kind`` selects.

    ``metrics`` counts fallbacks.

    A non-empty ``config.join_patterns`` wraps the splitter in a ``RuledSplitter``.

    Returns:
        The ``SentenceSplitter`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, an empty default language, a ``max_chars`` below
            one or an invalid join pattern (``INVALID_INPUT``).

    """
    if config.kind != SentenceSplitterKind.SENTENCIZER:
        msg = f"unknown sentence splitter kind: {config.kind!r}"
        raise AppValueError(msg)
    if not config.default_language or config.max_chars < 1:
        msg = "the sentence splitter needs a default language and max_chars of at least 1"
        raise AppValueError(msg)
    backend = importlib.import_module(
        "techai_webutils.clients.sentences.sentencizer.splitter"
    )
    splitter = cast("type[SentencizerSplitter]", backend.SentencizerSplitter)
    built: SentenceSplitter = splitter(
        default_language=config.default_language,
        max_chars=config.max_chars,
        metrics=metrics,
    )
    if config.join_patterns:
        return RuledSplitter(built, config.join_patterns)
    return built
