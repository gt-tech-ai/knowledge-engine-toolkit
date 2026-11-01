"""Unit tests for the ``SafetyCheck`` contract's value types and its blocked-error mapping."""

from __future__ import annotations

import dataclasses

import pytest

from techai_webutils.core.errors import ErrorCode
from techai_webutils.core.interfaces.safety import (
    SafetyDecision,
    SafetyResult,
    ScanKind,
    blocked_error,
)


def test_block_decision_carries_forbidden_code() -> None:
    """Test that a blocked scan maps to a coded ``FORBIDDEN`` error naming its flags.

    **Why this test is important:**
      - A safety block must reach the caller as a permission denial (gRPC 7 / HTTP 403), never
        a generic failure a client would retry; the flags tell the audit trail why.

    **What it tests:**
      - ``blocked_error`` returns an ``AppError`` with code ``FORBIDDEN``
      - its details carry the comma-joined flags
    """
    result = SafetyResult(
        flags=("pii.ssn", "pii.email"), decision=SafetyDecision.BLOCK, redacted=None
    )

    error = blocked_error(result)

    assert error.code is ErrorCode.FORBIDDEN
    assert error.details == {"flags": "pii.ssn,pii.email"}


def test_safety_result_is_frozen_and_kinds_are_stable() -> None:
    """Test that a scan result is immutable and the enum values are the wire strings.

    **Why this test is important:**
      - The result is read by the proxy and written to the audit record; the enum values are
        persisted, so renaming one would break every stored record.

    **What it tests:**
      - assigning ``SafetyResult.decision`` raises ``FrozenInstanceError``
      - ``ScanKind`` is ``prompt`` / ``answer``; ``SafetyDecision`` is ``allow`` / ``redact`` /
        ``block``
    """
    result = SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)

    with pytest.raises(dataclasses.FrozenInstanceError):
        result.decision = SafetyDecision.BLOCK  # pyright: ignore[reportAttributeAccessIssue]
    assert [kind.value for kind in ScanKind] == ["prompt", "answer"]
    assert [decision.value for decision in SafetyDecision] == ["allow", "redact", "block"]
