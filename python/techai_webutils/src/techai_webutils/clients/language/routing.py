"""Language-identifier decorators: route short text to its own backend.

And floor the confidence.

``ShortTextRouter`` sends a text under ``short_chars`` characters (a query, an interview
answer) to the backend built for short text (``lingua``) and everything else to the
primary. ``ConfidenceFloor`` reports ``UNDETERMINED`` for a guess below
``min_confidence``, keeping the guess's confidence so the caller can still see how close
it came. The builder applies them from ``LanguageIdentifierConfig``; neither does I/O.
"""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.language_identifier import (
    UNDETERMINED,
    LanguageGuess,
    LanguageIdentifier,
)


class ShortTextRouter(LanguageIdentifier):
    """Identifies text under ``short_chars`` characters with ``short``.

    The rest with ``primary``.
    """

    def __init__(
        self, primary: LanguageIdentifier, short: LanguageIdentifier, *, short_chars: int
    ) -> None:
        """Route by length (stripped of surrounding whitespace).

        Args:
            primary: The identifier for text of ``short_chars`` characters or more.
            short: The identifier for shorter text.
            short_chars: The length below which text is short.

        """
        self._primary = primary
        self._short = short
        self._short_chars = short_chars

    @override
    def identify(self, text: str) -> LanguageGuess:
        """Return the guess of the backend ``text``'s length selects.

        Returns:
            The guess of the backend ``text``'s length selects.

        """
        backend = self._short if len(text.strip()) < self._short_chars else self._primary
        return backend.identify(text)


class ConfidenceFloor(LanguageIdentifier):
    """Reports ``UNDETERMINED`` for a guess below ``min_confidence``."""

    def __init__(self, inner: LanguageIdentifier, *, min_confidence: float) -> None:
        """Floor ``inner``'s guesses at ``min_confidence``.

        Args:
            inner: The identifier whose guesses are floored.
            min_confidence: The lowest confidence a reported language has, in ``[0, 1]``.

        """
        self._inner = inner
        self._min_confidence = min_confidence

    @override
    def identify(self, text: str) -> LanguageGuess:
        """Return ``inner``'s guess.

        Or ``UNDETERMINED`` at its confidence when below the floor.

        Returns:
            ``inner``'s guess.

        """
        guess = self._inner.identify(text)
        if guess.confidence >= self._min_confidence:
            return guess
        return LanguageGuess(UNDETERMINED, guess.confidence)
