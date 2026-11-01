"""Config-selected ``SafetyCheck`` factory (the ``clients/llm/builder.py`` pattern).

``SafetyConfig.kind`` selects ``noop`` (pass everything, the default), ``regex`` (deterministic
PII, no infra), ``comprehend`` or ``bedrock_guardrails`` (managed; aiobotocore is imported only in
their branch and the client opens on the first scan). ``on_detect`` is the policy applied to a
detection. An unknown kind or policy, or a guardrail kind with no guardrail id, raises
``AppValueError`` (a ``ValueError`` coded ``INVALID_INPUT``).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.safety.decision import OnDetect
from techai_webutils.clients.safety.noop import NoopSafetyCheck
from techai_webutils.clients.safety.regex import RegexPiiSafetyCheck
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.safety import SafetyCheck


class SafetyKind(StrEnum):
    """The safety check backends."""

    NOOP = "noop"
    """Pass everything, flag nothing (the default; today's behaviour)."""
    REGEX = "regex"
    """Deterministic SSN / email / phone detection (no infrastructure)."""
    COMPREHEND = "comprehend"
    """AWS Comprehend PII detection."""
    BEDROCK_GUARDRAILS = "bedrock_guardrails"
    """A Bedrock guardrail (content filters, denied topics, PII, word lists)."""


@dataclass(frozen=True, slots=True)
class SafetyConfig:
    """Safety check configuration (one per chain: input and output are built separately)."""

    kind: str = SafetyKind.NOOP
    """A ``SafetyKind`` value."""
    on_detect: str = OnDetect.FLAG
    """An ``OnDetect`` value: the policy applied when a scan raises a flag."""
    region: str = "us-east-1"
    """AWS region for the managed kinds."""
    guardrail_id: str = ""
    """The Bedrock guardrail identifier (required for ``bedrock_guardrails``)."""
    guardrail_version: str = "DRAFT"
    """The Bedrock guardrail version (``DRAFT`` or a published number)."""
    timeout_seconds: float = 5.0
    """Per-scan deadline of a managed kind's AWS call (a hung call fails ``UNAVAILABLE``)."""


def _on_detect(value: str) -> OnDetect:
    """Return the ``OnDetect`` policy named by ``value``, or raise ``AppValueError``."""
    try:
        return OnDetect(value)
    except ValueError:
        msg = f"unknown safety on_detect policy: {value!r}"
        raise AppValueError(msg) from None


def new_safety_check_from_config(
    config: SafetyConfig, *, session: object | None = None
) -> SafetyCheck:
    """Build the ``SafetyCheck`` selected by ``config.kind``.

    Args:
        config: The safety configuration.
        session: The aiobotocore session the managed kinds open their client from; ``None``
            creates one (the dev/test kinds never touch it).

    Raises:
        AppValueError: an unknown kind or policy, or ``bedrock_guardrails`` without an id.

    """
    on_detect = _on_detect(config.on_detect)
    if config.kind == SafetyKind.NOOP:
        return NoopSafetyCheck()
    if config.kind == SafetyKind.REGEX:
        return RegexPiiSafetyCheck(on_detect)
    if config.kind not in {SafetyKind.COMPREHEND, SafetyKind.BEDROCK_GUARDRAILS}:
        msg = f"unknown safety kind: {config.kind!r}"
        raise AppValueError(msg)
    if config.kind == SafetyKind.BEDROCK_GUARDRAILS and not config.guardrail_id:
        msg = "bedrock_guardrails safety kind requires a guardrail_id"
        raise AppValueError(msg)
    # Lazy imports: aiobotocore loads only when a managed kind is selected.
    import aiobotocore.session  # type: ignore[import-untyped]  # noqa: PLC0415

    from techai_webutils.clients.safety.aws_client import LazyAwsClient  # noqa: PLC0415

    aws_session: object = (
        session if session is not None else aiobotocore.session.get_session()
    )
    if config.kind == SafetyKind.COMPREHEND:
        from techai_webutils.clients.safety.comprehend import (  # noqa: PLC0415
            ComprehendSafetyCheck,
        )

        client = LazyAwsClient(
            aws_session, "comprehend", config.region, timeout_s=config.timeout_seconds
        )
        return ComprehendSafetyCheck(client, on_detect)
    from techai_webutils.clients.safety.bedrock_guardrails import (  # noqa: PLC0415
        BedrockGuardrailsSafetyCheck,
    )

    client = LazyAwsClient(
        aws_session, "bedrock-runtime", config.region, timeout_s=config.timeout_seconds
    )
    return BedrockGuardrailsSafetyCheck(
        client, config.guardrail_id, config.guardrail_version, on_detect
    )
