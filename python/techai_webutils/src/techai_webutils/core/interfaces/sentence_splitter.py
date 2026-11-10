"""``SentenceSplitter``: sentence boundaries, in process.

The sentence chunker packs sentences into token-sized chunks, so it needs boundaries for
every block it chunks: far too many calls to send over the network (the NLP cost review:
sentence splitting runs in the stage that needs it, never through the NLP service). A
splitter returns ``TextSpan``s that tile the text: the first starts at 0, each starts
where the previous ends, and the last ends at ``len(text)``, so no character is lost
between sentences. Empty text has no spans.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass

from techai_webutils.core.errors import AppValueError


@dataclass(frozen=True, slots=True)
class TextSpan:
    """A half-open character range ``[start, end)`` of a text."""

    start: int
    """Offset of the first character."""
    end: int
    """Offset one past the last character."""

    def __post_init__(self) -> None:
        """Refuse a negative start or an empty or inverted range.

        Raises:
            AppValueError: ``start < 0`` or ``end <= start`` (``INVALID_INPUT``).

        """
        if self.start < 0 or self.end <= self.start:
            msg = f"invalid text span [{self.start}, {self.end})"
            raise AppValueError(msg)


class SentenceSplitter(ABC):
    """Splits a text into sentences."""

    @abstractmethod
    def split(self, text: str, *, language: str | None = None) -> list[TextSpan]:
        """Return ``text``'s sentences as spans that tile it (none for empty text).

        ``language`` (an ISO 639-1 code) selects that language's rules where the splitter
        has them; ``None`` uses the splitter's default.

        Args:
            text: The text to split.
            language: The text's language code, or ``None`` to let the splitter decide.

        Returns:
            The sentences as spans that tile ``text``; empty for empty text.

        """
