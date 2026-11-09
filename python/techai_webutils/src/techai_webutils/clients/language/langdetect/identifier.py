"""``LangdetectIdentifier``: langdetect, seeded so a text always gets the same answer.

The same library and seed as the ``langdetect`` metadata extractor (today's document
language), so the default kind reports what documents carry today. Its language profiles
load at import (single-threaded, at the composition root), so concurrent first calls
cannot race langdetect's unguarded lazy load.
"""

from __future__ import annotations

import contextlib
from typing import override

from langdetect import DetectorFactory, LangDetectException, detect_langs

from techai_webutils.core.interfaces.language_identifier import (
    UNDETERMINED_GUESS,
    LanguageGuess,
    LanguageIdentifier,
)

DetectorFactory.seed = 0

with contextlib.suppress(LangDetectException):
    detect_langs("The quick brown fox jumps over the lazy dog.")


class LangdetectIdentifier(LanguageIdentifier):
    """Identifies languages with langdetect (ISO 639-1 codes; ``zh-cn`` and ``zh-tw``)."""

    @override
    def identify(self, text: str) -> LanguageGuess:
        """Return langdetect's most probable language, or ``UNDETERMINED_GUESS``.

        Returns:
            Langdetect's most probable language, or ``UNDETERMINED_GUESS``.

        """
        stripped = text.strip()
        if not stripped:
            return UNDETERMINED_GUESS
        try:
            best = detect_langs(stripped)[0]
        except LangDetectException:
            return UNDETERMINED_GUESS
        return LanguageGuess(str(best.lang), min(1.0, float(best.prob)))
