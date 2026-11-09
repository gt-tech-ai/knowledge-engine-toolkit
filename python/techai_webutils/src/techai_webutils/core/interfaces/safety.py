"""``SafetyCheck`` — the swappable PII / content-safety scanner.

A check scans one text — the prompt before a model call, or the answer after — and returns
a ``SafetyResult``: the flags it raised and what to do about them (``allow``, ``redact``
with a rewritten text, or ``block``). A ``block`` reaches the caller as ``blocked_error``:
a coded ``FORBIDDEN``, never a new code. Managed backends own an SDK client, so a check is
an async context manager (``ManagedResource``); infra-free checks inherit the no-op
lifecycle here.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from enum import StrEnum
from typing import Self

from techai_webutils.core.errors.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.lifecycle import ManagedResource


class ScanKind(StrEnum):
    """Which side of the model call a text is."""

    PROMPT = "prompt"
    """The user's prompt, scanned before the model call."""
    ANSWER = "answer"
    """The model's answer, scanned after the call."""


class SafetyDecision(StrEnum):
    """What a scan decided to do with the text."""

    ALLOW = "allow"
    """Serve the text unchanged (flags, if any, are recorded only)."""
    REDACT = "redact"
    """Serve ``SafetyResult.redacted`` instead of the text."""
    BLOCK = "block"
    """Refuse the request (``blocked_error`` → ``FORBIDDEN``)."""


@dataclass(frozen=True, slots=True)
class SafetyResult:
    """One scan's verdict."""

    flags: tuple[str, ...]
    """What was detected, as dotted names (``pii.ssn``, ``content.violence``), in
    detection order; empty when the text is clean."""
    decision: SafetyDecision
    """What to do with the text."""
    redacted: str | None
    """The rewritten text when ``decision`` is ``redact``; otherwise ``None``."""


def blocked_error(result: SafetyResult) -> AppError:
    """Return the coded ``FORBIDDEN`` error a blocked scan surfaces as.

    Naming its flags.
    """
    return AppError(
        ErrorCode.FORBIDDEN,
        "request blocked by safety policy",
        details={"flags": ",".join(result.flags)},
    )


class SafetyCheck(ManagedResource, ABC):
    """Scans a prompt or an answer for PII and unsafe content."""

    @abstractmethod
    async def scan(self, text: str, *, kind: ScanKind) -> SafetyResult:
        """Scan ``text`` and return the verdict.

        Raises:
            AppError: a managed backend's call failed (``UNAVAILABLE`` when transient).

        """

    async def aclose(self) -> None:
        """Release any owned client (infra-free checks own none)."""

    async def __aenter__(self) -> Self:
        """Enter the async context; a managed client opens lazily on first scan."""
        return self

    async def __aexit__(self, *exc: object) -> None:
        """Release any owned client on context exit."""
        await self.aclose()
