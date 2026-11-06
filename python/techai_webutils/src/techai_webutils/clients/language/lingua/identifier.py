"""``LinguaIdentifier``: Lingua, the language identifier that stays accurate on short text.

Lingua (``lingua-language-detector``, the ``lingua`` extra) reads a few words well, where
n-gram profilers such as langdetect guess, so it serves queries and interview answers
(``ShortTextRouter``). Its detector loads on the first ``identify`` (construction does no
I/O), under a lock so concurrent first calls build it once. Codes are ISO 639-1, lowercased.
"""

from __future__ import annotations

import importlib
import threading
from typing import TYPE_CHECKING, Protocol, cast, override

from techai_webutils.core.interfaces.language_identifier import (
    UNDETERMINED_GUESS,
    LanguageGuess,
    LanguageIdentifier,
)

if TYPE_CHECKING:
    from collections.abc import Sequence


class _IsoCode(Protocol):
    """A Lingua ISO code enum member."""

    @property
    def name(self) -> str:
        """The code, upper-case (``EN``)."""
        ...


class _Language(Protocol):
    """A Lingua ``Language``."""

    @property
    def iso_code_639_1(self) -> _IsoCode:
        """The language's ISO 639-1 code."""
        ...


class _Confidence(Protocol):
    """A Lingua ``ConfidenceValue``."""

    @property
    def language(self) -> _Language:
        """The language."""
        ...

    @property
    def value(self) -> float:
        """Its confidence, in ``[0, 1]``."""
        ...


class LinguaDetector(Protocol):
    """The part of a Lingua ``LanguageDetector`` this identifier calls."""

    def compute_language_confidence_values(self, text: str) -> Sequence[_Confidence]:
        """Return every language's confidence for ``text``, most confident first."""
        ...


class LinguaIdentifier(LanguageIdentifier):
    """Identifies languages with Lingua, over all the languages it knows."""

    def __init__(self) -> None:
        """Build the detector on first use."""
        self._detector: LinguaDetector | None = None
        self._lock = threading.Lock()

    @override
    def identify(self, text: str) -> LanguageGuess:
        """Return Lingua's most confident language, or ``UNDETERMINED_GUESS``."""
        stripped = text.strip()
        if not stripped:
            return UNDETERMINED_GUESS
        values = self._loaded().compute_language_confidence_values(stripped)
        if not values:
            return UNDETERMINED_GUESS
        best = values[0]
        return LanguageGuess(
            best.language.iso_code_639_1.name.lower(),
            min(1.0, max(0.0, float(best.value))),
        )

    def _loaded(self) -> LinguaDetector:
        """Return the detector, building it once."""
        with self._lock:
            detector = self._detector
            if detector is None:
                lingua = importlib.import_module("lingua")
                builder = lingua.LanguageDetectorBuilder.from_all_languages()
                detector = cast("LinguaDetector", builder.build())
                self._detector = detector
            return detector
