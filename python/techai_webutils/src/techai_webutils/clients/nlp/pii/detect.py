"""Personal-data spans (the ``pii`` task) and the helper that redacts or masks them.

``find_pii`` finds e-mail addresses, phone numbers, US social security numbers, payment
card numbers (13 to 19 digits that pass the Luhn check) and IPv4 addresses with patterns,
in process, with no model. It finds the formats, not the meaning: a name or an address
needs an NER
    model's
``PERSON`` / ``LOC`` entities. ``apply_pii`` rewrites a text so no detected span is left:
``redact`` puts ``[EMAIL]``-style placeholders in their place (offsets after them shift),
``mask`` replaces each character with ``*`` (offsets kept), ``keep`` returns the text
unchanged.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import TaskResult

if TYPE_CHECKING:
    from collections.abc import Sequence

PII = "pii"
"""The task name; register it with ``registry.register(PII, PiiSpan)``."""

_CARD_MIN_DIGITS = 13
"""The fewest digits in a payment card number."""
_PHONE_DIGITS = range(10, 16)
"""How many digits a phone number has (ITU E.164 allows up to 15)."""
_PHONE_SEPARATORS = frozenset(" .-()")
"""A phone number is written with one of these or a leading ``+``; a bare digit run is an
id."""
_LUHN_BASE = 10
"""Luhn works on decimal digits: a doubled digit over 9 counts its digit sum."""


class PiiKind(StrEnum):
    """What kind of personal data a span holds."""

    EMAIL = "EMAIL"
    """An e-mail address."""
    PHONE = "PHONE"
    """A phone number: 10 to 15 digits written with a leading ``+`` or with spaces, dots,
    dashes or brackets (a bare run of digits is taken for an id, not a phone)."""
    SSN = "SSN"
    """A US social security number (``123-45-6789``)."""
    CARD = "CARD"
    """A payment card number that passes the Luhn check."""
    IP_ADDRESS = "IP_ADDRESS"
    """An IPv4 address."""
    PERSON = "PERSON"
    """A person's name. ``find_pii`` never returns it: the consumer adds it from an NER
    model."""


class PiiMode(StrEnum):
    """What ``apply_pii`` does with each span."""

    REDACT = "redact"
    """Replace the span with ``[KIND]``."""
    MASK = "mask"
    """Replace each character of the span with ``*``."""
    KEEP = "keep"
    """Leave the text unchanged."""


@dataclass(frozen=True, slots=True)
class PiiSpan(TaskResult):
    """One personal-data span: its kind and its ``[start, end)`` range in the block."""

    kind: PiiKind
    """What the span holds."""
    start: int
    """Offset of the first character."""
    end: int
    """Offset one past the last character."""
    text: str
    """The block's text over the span."""

    def __post_init__(self) -> None:
        """Refuse an empty or inverted span.

        Raises:
            AppValueError: ``start < 0`` or ``end <= start`` (``INVALID_INPUT``).

        """
        if self.start < 0 or self.end <= self.start:
            msg = f"pii span [{self.start}, {self.end}) is invalid"
            raise AppValueError(msg)


_OCTET = r"(?:25[0-5]|2[0-4]\d|1?\d?\d)"
_PATTERNS: tuple[tuple[PiiKind, re.Pattern[str]], ...] = (
    (
        PiiKind.EMAIL,
        re.compile(r"(?<![\w.+-])[\w.+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+"),
    ),
    (PiiKind.SSN, re.compile(r"(?<!\d)\d{3}-\d{2}-\d{4}(?!\d)")),
    (PiiKind.CARD, re.compile(r"(?<!\d)(?:\d[ -]?){12,18}\d(?!\d)")),
    (PiiKind.IP_ADDRESS, re.compile(rf"(?<![\d.]){_OCTET}(?:\.{_OCTET}){{3}}(?!\.?\d)")),
    (
        PiiKind.PHONE,
        re.compile(r"(?<![\w+])\+?\(?\d{1,4}\)?(?:[ .-]?\(?\d{2,4}\)?){2,4}(?!\w)"),
    ),
)
"""Each kind's pattern, most specific first (an earlier kind's span wins an overlap)."""


def find_pii(text: str) -> list[PiiSpan]:
    """Return the personal-data spans in ``text``, in text order, never overlapping."""
    spans: list[PiiSpan] = []
    for kind, pattern in _PATTERNS:
        for match in pattern.finditer(text):
            if not _valid(kind, match.group(0)):
                continue
            if any(match.start() < s.end and s.start < match.end() for s in spans):
                continue
            spans.append(PiiSpan(kind, match.start(), match.end(), match.group(0)))
    return sorted(spans, key=lambda s: s.start)


def _valid(kind: PiiKind, found: str) -> bool:
    """Apply the checks a pattern cannot express (card Luhn sum, phone digit count)."""
    digits = [int(c) for c in found if c.isdigit()]
    if kind == PiiKind.CARD:
        return len(digits) >= _CARD_MIN_DIGITS and _luhn(digits)
    if kind == PiiKind.PHONE:
        formatted = found.startswith("+") or any(c in _PHONE_SEPARATORS for c in found)
        return formatted and len(digits) in _PHONE_DIGITS
    return True


def _luhn(digits: list[int]) -> bool:
    """Whether ``digits`` pass the Luhn checksum."""
    total = 0
    for position, digit in enumerate(reversed(digits)):
        doubled = digit * 2 if position % 2 else digit
        total += doubled - (_LUHN_BASE - 1) if doubled >= _LUHN_BASE else doubled
    return total % _LUHN_BASE == 0


def apply_pii(text: str, spans: Sequence[PiiSpan], mode: PiiMode) -> str:
    """Return ``text`` with every span redacted, masked, or (``keep``) left alone.

    Raises:
        AppValueError: An unknown mode (``INVALID_INPUT``).

    """
    if mode == PiiMode.KEEP:
        return text
    if mode not in {PiiMode.REDACT, PiiMode.MASK}:
        msg = f"unknown pii mode: {mode!r}"
        raise AppValueError(msg)
    parts: list[str] = []
    position = 0
    for span in sorted(spans, key=lambda s: s.start):
        start = max(span.start, position)
        if start >= span.end:
            continue
        parts.append(text[position:start])
        parts.append(
            f"[{span.kind}]" if mode == PiiMode.REDACT else "*" * (span.end - start)
        )
        position = span.end
    parts.append(text[position:])
    return "".join(parts)
