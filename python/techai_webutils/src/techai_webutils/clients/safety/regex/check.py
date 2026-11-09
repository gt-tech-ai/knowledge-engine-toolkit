"""The ``regex`` safety check: deterministic US PII detection (SSN, email, phone).

No infra.

Patterns are anchored on non-word boundaries so a match inside a longer token (an id, a
hash) is not a detection. ``redact`` replaces each match with ``[REDACTED:<type>]``.
"""

from __future__ import annotations

import re
from typing import TYPE_CHECKING, override

from techai_webutils.clients.safety.decision import decide
from techai_webutils.core.interfaces.safety import SafetyCheck, SafetyResult, ScanKind

if TYPE_CHECKING:
    from techai_webutils.clients.safety.decision import OnDetect

_PATTERNS: tuple[tuple[str, re.Pattern[str]], ...] = (
    ("ssn", re.compile(r"(?<!\w)\d{3}-\d{2}-\d{4}(?!\w)")),
    ("email", re.compile(r"(?<![\w.%+-])[\w.%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}(?!\w)")),
    (
        "phone",
        re.compile(
            r"(?<!\w)(?:\+?1[-.\s]?)?(?:\(\d{3}\)|\d{3})[-.\s]?\d{3}[-.\s]?\d{4}(?!\w)"
        ),
    ),
)
"""Each PII type with its pattern, in the order flags are reported and redaction runs."""


class RegexPiiSafetyCheck(SafetyCheck):
    """Flags ``pii.ssn``, ``pii.email`` and ``pii.phone`` in prompts and answers alike."""

    def __init__(self, on_detect: OnDetect) -> None:
        """Set the policy applied when a pattern matches."""
        self._on_detect = on_detect

    @override
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Return the verdict for ``text`` (``kind`` does not change the patterns)."""
        flags: list[str] = []
        redacted = text
        for name, pattern in _PATTERNS:
            redacted, count = pattern.subn(f"[REDACTED:{name}]", redacted)
            if count:
                flags.append(f"pii.{name}")
        return decide(tuple(flags), self._on_detect, redacted)
