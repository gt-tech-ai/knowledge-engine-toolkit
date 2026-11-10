"""``OnDetect`` and the shared detection → ``SafetyResult`` rule every backend applies."""

from __future__ import annotations

from enum import StrEnum

from techai_webutils.core.interfaces.safety import SafetyDecision, SafetyResult


class OnDetect(StrEnum):
    """The policy applied when a scan raises a flag."""

    FLAG = "flag"
    """Record the flags and serve the text unchanged."""
    REDACT = "redact"
    """Serve the text with each detection replaced."""
    BLOCK = "block"
    """Refuse the request."""


_CLEAN = SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)
"""The verdict for a text with no detections, under every policy."""


def decide(flags: tuple[str, ...], on_detect: OnDetect, redacted: str) -> SafetyResult:
    """Return the verdict for ``flags`` under ``on_detect``.

    ``redacted`` is used for ``redact``.

    Args:
        flags: What the check found.
        on_detect: What a check does when it detects something: flag, redact or block.
        redacted: The text with the findings redacted.

    Returns:
        The verdict for ``flags`` under ``on_detect``.

    """
    if not flags:
        return _CLEAN
    if on_detect is OnDetect.REDACT:
        return SafetyResult(
            flags=flags, decision=SafetyDecision.REDACT, redacted=redacted
        )
    if on_detect is OnDetect.BLOCK:
        return SafetyResult(flags=flags, decision=SafetyDecision.BLOCK, redacted=None)
    return SafetyResult(flags=flags, decision=SafetyDecision.ALLOW, redacted=None)
