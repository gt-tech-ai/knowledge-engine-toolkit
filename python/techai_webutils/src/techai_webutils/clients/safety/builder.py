"""Config-selected ``SafetyCheck`` factory (the ``clients/llm/builder.py`` pattern).

``SafetyConfig.kind`` selects ``noop`` (pass everything, the default), ``regex``
(deterministic PII, no infra), ``comprehend`` or ``bedrock_guardrails`` (managed;
aiobotocore is imported only in their branch and the client opens on the first scan).
``on_detect`` is the policy applied to a detection. An unknown kind or policy, a guardrail
kind with no guardrail id, or a managed kind whose ``timeout_seconds`` is not positive
raises ``AppValueError`` (a ``ValueError`` coded ``INVALID_INPUT``).
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.safety.decision import OnDetect
from techai_webutils.clients.safety.noop import NoopSafetyCheck
from techai_webutils.clients.safety.regex import RegexPiiSafetyCheck
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Callable

    from techai_webutils.clients.safety.aws_client import (
        LazyAwsClient as _LazyLazyAwsClient,
    )
    from techai_webutils.clients.safety.bedrock_guardrails import (
        BedrockGuardrailsSafetyCheck as _LazyBedrockGuardrailsSafetyCheck,
    )
    from techai_webutils.clients.safety.comprehend import (
        ComprehendSafetyCheck as _LazyComprehendSafetyCheck,
    )
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
    """Safety check configuration.

    (one per chain: input and output are built separately).
    """

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
    """Per-scan deadline of a managed kind's AWS call (a hung call fails
    ``UNAVAILABLE``)."""


def _on_detect(value: str) -> OnDetect:
    """Return the ``OnDetect`` policy named by ``value``, or raise ``AppValueError``.

    Args:
        value: The configured policy name.

    Returns:
        The ``OnDetect`` policy named by ``value``, or raise ``AppValueError``.

    Raises:
        AppValueError: Unknown safety on_detect policy: ``value``.

    """
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
        session: The aiobotocore session the managed kinds open their client from;
            ``None`` creates one (the dev/test kinds never touch it).

    Returns:
        The ``SafetyCheck`` selected by ``config.kind``.

    Raises:
        AppValueError: an unknown kind or policy, ``bedrock_guardrails`` without an id, or
            a managed kind whose ``timeout_seconds`` is not positive.

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
    if config.timeout_seconds <= 0:
        msg = (
            f"managed safety kind needs a positive timeout_seconds, got "
            f"{config.timeout_seconds}"
        )
        raise AppValueError(msg)
    # Lazy imports: aiobotocore loads only when a managed kind is selected.
    aiobotocore_session = importlib.import_module("aiobotocore.session")

    lazy_aws_client_cls = cast(
        "type[_LazyLazyAwsClient]",
        importlib.import_module(
            "techai_webutils.clients.safety.aws_client"
        ).LazyAwsClient,
    )

    aws_session: object = (
        session
        if session is not None
        else cast("Callable[[], object]", aiobotocore_session.get_session)()
    )
    if config.kind == SafetyKind.COMPREHEND:
        comprehend_safety_check_cls = cast(
            "type[_LazyComprehendSafetyCheck]",
            importlib.import_module(
                "techai_webutils.clients.safety.comprehend"
            ).ComprehendSafetyCheck,
        )

        client = lazy_aws_client_cls(
            aws_session, "comprehend", config.region, timeout_s=config.timeout_seconds
        )
        return comprehend_safety_check_cls(client, on_detect)
    bedrock_guardrails_safety_check_cls = cast(
        "type[_LazyBedrockGuardrailsSafetyCheck]",
        importlib.import_module(
            "techai_webutils.clients.safety.bedrock_guardrails"
        ).BedrockGuardrailsSafetyCheck,
    )

    client = lazy_aws_client_cls(
        aws_session, "bedrock-runtime", config.region, timeout_s=config.timeout_seconds
    )
    return bedrock_guardrails_safety_check_cls(
        client, config.guardrail_id, config.guardrail_version, on_detect
    )
