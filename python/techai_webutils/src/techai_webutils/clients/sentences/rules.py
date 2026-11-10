"""``RuledSplitter``: per-document-type join rules over any ``SentenceSplitter``.

A generic splitter cuts after every sentence-final mark, so a transcript's ``Q.`` / ``A.``
markers, a speaker label (``WITNESS:``) or a trailing honorific (``... asked Dr.``) become
sentences of their own. ``RuledSplitter`` joins a span whose stripped text fully matches
one of its patterns onto the span after it; the spans still tile the text. With no
patterns it returns the inner splitter's spans unchanged.
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter, TextSpan

if TYPE_CHECKING:
    from collections.abc import Sequence

TRANSCRIPT_JOIN_PATTERNS: tuple[str, ...] = (
    r"[QA]\.",
    r"[A-Z][A-Za-z .'-]{0,40}:",
    r".*\b(?i:Mr|Mrs|Ms|Dr|Prof|Hon|Rev|St)\.",
)
"""Transcript rules: a ``Q.`` / ``A.`` marker, a speaker label, a span ending in an
honorific."""


@final
class RuledSplitter(SentenceSplitter):
    """Joins each span that fully matches a join pattern onto the next span."""

    def __init__(self, inner: SentenceSplitter, join_patterns: Sequence[str]) -> None:
        """Wrap ``inner`` with ``join_patterns`` (regular expressions, matched in full).

        Args:
            inner: The splitter whose sentences are joined.
            join_patterns: Regexes of sentences that join the next one.

        Raises:
            AppValueError: A pattern is not a valid regular expression
                (``INVALID_INPUT``).

        """
        self._inner = inner
        try:
            self._patterns = tuple(re.compile(p, re.DOTALL) for p in join_patterns)
        except re.error as exc:
            msg = f"invalid sentence join pattern: {exc}"
            raise AppValueError(msg) from exc

    @override
    def split(self, text: str, *, language: str | None = None) -> list[TextSpan]:
        """Return ``inner``'s spans with every matching span joined onto the next.

        Args:
            text: The text to split.
            language: The text's language code, or ``None`` to let the splitter decide.

        Returns:
            ``inner``'s spans with every matching span joined onto the next.

        """
        spans = self._inner.split(text, language=language)
        if not self._patterns:
            return spans
        joined: list[TextSpan] = []
        start: int | None = None
        for span in spans:
            first = span.start if start is None else start
            if self._joins(text[span.start : span.end].strip()):
                start = first
                continue
            joined.append(TextSpan(first, span.end))
            start = None
        if start is not None:
            joined.append(TextSpan(start, spans[-1].end))
        return joined

    def _joins(self, sentence: str) -> bool:
        """Whether ``sentence`` fully matches a join pattern.

        Args:
            sentence: The sentence.

        Returns:
            Whether ``sentence`` fully matches a join pattern.

        """
        return any(p.fullmatch(sentence) for p in self._patterns)
