"""``SentencizerSplitter``: spaCy's rule-based ``sentencizer`` on a blank pipeline.

Per language.

A blank pipeline is a language's tokenizer rules and nothing else (no model, no download);
the ``sentencizer`` ends a sentence at its punctuation characters, and the tokenizer's
exceptions keep abbreviations (``Dr.``, ``p.m.``) and decimals (``3.50``) whole. On one
core it splits about 270,000 words/s (spaCy 3.8, measured 2026-11-05), about 280 times
pysbd's rate.

Each language's pipeline is built on its first use and kept. A language spaCy has no rules
for, or whose tokenizer needs a library that is not installed (``ja``, ``ko``, ``th``,
``vi``), falls back to the multi-language ``xx`` rules, counted in
``sentence_splitter_fallback_total{language}``. A pipeline runs under a lock, because a
spaCy tokenizer is not safe to share across threads.
"""

from __future__ import annotations

import threading
from itertools import pairwise, starmap
from typing import TYPE_CHECKING, override

import spacy

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter, TextSpan

if TYPE_CHECKING:
    from spacy.language import Language

    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider

FALLBACK_LANGUAGE = "xx"
"""spaCy's multi-language rules, used where a language has none."""
FALLBACK_METRIC = "sentence_splitter_fallback_total"
"""Pipelines built with the ``xx`` rules because the language had none, by language."""


class SentencizerSplitter(SentenceSplitter):
    """Splits sentences with a blank spaCy pipeline and its ``sentencizer``."""

    def __init__(
        self,
        *,
        default_language: str,
        max_chars: int,
        metrics: MetricsProvider | None = None,
    ) -> None:
        """Split with ``default_language``'s rules unless ``split`` names one.

        ``max_chars`` is the longest text accepted; ``metrics`` counts fallbacks to
        ``xx``.
        """
        self._default = default_language
        self._max_chars = max_chars
        self._pipelines: dict[str, tuple[Language, threading.Lock]] = {}
        self._build_lock = threading.Lock()
        self._fallbacks: MetricCounter | None = (
            metrics.counter(
                FALLBACK_METRIC,
                "Sentence-splitter pipelines that fell back to the multi-language rules.",
                ["language"],
            )
            if metrics is not None
            else None
        )

    @override
    def split(self, text: str, *, language: str | None = None) -> list[TextSpan]:
        """Return ``text``'s sentences as spans that tile it.

        Raises:
            AppValueError: ``text`` is longer than ``max_chars`` (``INVALID_INPUT``).

        """
        if not text:
            return []
        if len(text) > self._max_chars:
            msg = (
                f"text of {len(text)} characters exceeds the splitter's {self._max_chars}"
            )
            raise AppValueError(msg)
        nlp, lock = self._pipeline(language or self._default)
        with lock:
            starts = [sent.start_char for sent in nlp(text).sents]
        bounds = [0, *(s for s in starts if s > 0), len(text)]
        return list(starmap(TextSpan, pairwise(bounds)))

    def _pipeline(self, language: str) -> tuple[Language, threading.Lock]:
        """Return ``language``'s pipeline and its lock, building them on first use."""
        with self._build_lock:
            found = self._pipelines.get(language)
            if found is None:
                found = (self._build(language), threading.Lock())
                self._pipelines[language] = found
            return found

    def _build(self, language: str) -> Language:
        """Build a blank pipeline with a sentencizer, falling back to ``xx``."""
        try:
            nlp = spacy.blank(language)
        except ImportError:
            if self._fallbacks is not None:
                self._fallbacks.inc(1.0, language=language)
            nlp = spacy.blank(FALLBACK_LANGUAGE)
        nlp.add_pipe("sentencizer")
        nlp.max_length = self._max_chars
        return nlp
