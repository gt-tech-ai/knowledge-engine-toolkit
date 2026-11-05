"""``LanguageIdentifier``: a text's language, in process.

Enrich tags every block with its language, so identification runs where it is needed, never
through the NLP service (the NLP cost review). Each backend reports its model's own codes:
``langdetect`` ISO 639-1 (``en``, ``zh-cn``), the ``fasttext`` ``lid.176`` model ISO 639-1
(``en``), and ``glotlid`` ISO 639-3 with the script separately (``eng`` + ``Latn``). Text with no
words, or that the backend cannot identify, is ``UNDETERMINED`` with confidence 0.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.errors import AppValueError

UNDETERMINED = "und"
"""The code for text whose language is unknown (ISO 639-2 ``und``)."""


@dataclass(frozen=True, slots=True)
class LanguageGuess:
    """A text's most likely language."""

    code: str
    """The language code, in the backend's code system (see the module docs)."""
    confidence: float
    """The backend's probability for ``code``, in ``[0, 1]``."""
    script: str = ""
    """The ISO 15924 script (``Latn``) where the backend reports one; empty otherwise."""

    def __post_init__(self) -> None:
        """Refuse an empty code or a confidence outside ``[0, 1]``.

        Raises:
            AppValueError: The code is empty or the confidence is out of range
                (``INVALID_INPUT``).

        """
        if not self.code or not 0.0 <= self.confidence <= 1.0:
            msg = f"invalid language guess {self.code!r} ({self.confidence})"
            raise AppValueError(msg)


UNDETERMINED_GUESS = LanguageGuess(UNDETERMINED, 0.0)
"""The guess for text whose language is unknown."""


class LanguageIdentifier(ABC):
    """Identifies the language of a text."""

    @abstractmethod
    def identify(self, text: str) -> LanguageGuess:
        """Return ``text``'s most likely language, or ``UNDETERMINED_GUESS``."""
