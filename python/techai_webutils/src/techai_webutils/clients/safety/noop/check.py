"""The ``noop`` safety check: passes every text and flags nothing (the default)."""

from __future__ import annotations

from typing import override

from techai_webutils.core.interfaces.safety import (
    SafetyCheck,
    SafetyDecision,
    SafetyResult,
    ScanKind,
)

_ALLOW = SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)
"""The noop verdict."""


class NoopSafetyCheck(SafetyCheck):
    """Passes everything, so wiring the safety tier changes nothing until a detector is chosen."""

    @override
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Return ``allow`` with no flags."""
        return _ALLOW
