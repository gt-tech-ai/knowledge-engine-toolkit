"""Unit tests for the managed safety kinds (Comprehend, Bedrock guardrails) over a mocked AWS SDK."""

from __future__ import annotations

from contextlib import asynccontextmanager
from typing import TYPE_CHECKING, Any
from unittest.mock import AsyncMock, MagicMock

import pytest
from botocore.exceptions import ClientError

from techai_webutils.clients.safety import (
    OnDetect,
    SafetyConfig,
    SafetyKind,
    new_safety_check_from_config,
)
from techai_webutils.clients.safety.bedrock_guardrails import BedrockGuardrailsSafetyCheck
from techai_webutils.clients.safety.comprehend import ComprehendSafetyCheck
from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.interfaces.safety import SafetyDecision, ScanKind

if TYPE_CHECKING:
    from collections.abc import AsyncGenerator

_TEXT = "SSN 123-45-6789 and jane@example.com"


def _session(client: Any) -> MagicMock:  # noqa: ANN401 - the mocked SDK client
    """Return a mocked aiobotocore session whose ``create_client`` yields ``client``."""

    @asynccontextmanager
    async def _open(*_args: object, **_kwargs: object) -> AsyncGenerator[Any]:
        yield client

    session = MagicMock()
    session.create_client = MagicMock(side_effect=_open)
    return session


@pytest.mark.asyncio
async def test_comprehend_maps_entities_to_flags() -> None:
    """Test that Comprehend's PII entities become flags and redactions.

    **Why this test is important:**
      - Comprehend is a managed staging detector; its entity types must map onto the same
        ``pii.<type>`` flags the regex kind emits so the audit trail reads one vocabulary.

    **What it tests:**
      - SSN and EMAIL entities (EMAIL twice) yield ``("pii.ssn", "pii.email")``
      - ``on_detect=redact`` replaces each entity span with ``[REDACTED:<type>]``
      - the client opens once across two scans, from the configured region, and closes on exit
    """
    client = MagicMock()
    client.detect_pii_entities = AsyncMock(
        return_value={
            "Entities": [
                {"Type": "SSN", "BeginOffset": 4, "EndOffset": 15, "Score": 0.99},
                {"Type": "EMAIL", "BeginOffset": 20, "EndOffset": 36, "Score": 0.98},
                {"Type": "EMAIL", "BeginOffset": 20, "EndOffset": 36, "Score": 0.97},
            ]
        }
    )
    session = _session(client)
    config = SafetyConfig(
        kind=SafetyKind.COMPREHEND, on_detect=OnDetect.REDACT, region="eu-west-1"
    )
    check = new_safety_check_from_config(config, session=session)

    async with check:
        first = await check.scan(_TEXT, kind=ScanKind.PROMPT)
        await check.scan(_TEXT, kind=ScanKind.ANSWER)

    assert isinstance(check, ComprehendSafetyCheck)
    assert first.flags == ("pii.ssn", "pii.email")
    assert first.decision is SafetyDecision.REDACT
    assert first.redacted == "SSN [REDACTED:ssn] and [REDACTED:email]"
    session.create_client.assert_called_once_with("comprehend", region_name="eu-west-1")
    client.detect_pii_entities.assert_awaited_with(Text=_TEXT, LanguageCode="en")


@pytest.mark.asyncio
async def test_guardrails_intervened_maps_to_decision() -> None:
    """Test that a guardrail intervention becomes flags and the configured decision.

    **Why this test is important:**
      - Only ``GUARDRAIL_INTERVENED`` is a detection; a pass-through must never block, and an
        intervention must carry why it fired into the audit trail.

    **What it tests:**
      - an intervention with content and PII assessments under ``block`` → decision ``block``
        with ``("content.violence", "pii.email")``, assessed as guardrail ``INPUT``
      - ``action: NONE`` → ``allow`` with no flags
      - an intervention with no itemised assessment under ``redact`` → the
        ``guardrail.intervened`` flag and the guardrail's masked output, assessed as ``OUTPUT``
    """
    client = MagicMock()
    client.apply_guardrail = AsyncMock(
        side_effect=[
            {
                "action": "GUARDRAIL_INTERVENED",
                "outputs": [{"text": "blocked"}],
                "assessments": [
                    {
                        "contentPolicy": {"filters": [{"type": "VIOLENCE"}]},
                        "sensitiveInformationPolicy": {
                            "piiEntities": [{"type": "EMAIL"}]
                        },
                    }
                ],
            },
            {"action": "NONE", "outputs": [], "assessments": []},
            {
                "action": "GUARDRAIL_INTERVENED",
                "outputs": [{"text": "masked"}],
                "assessments": [],
            },
        ]
    )
    session = _session(client)
    block = new_safety_check_from_config(
        SafetyConfig(
            kind=SafetyKind.BEDROCK_GUARDRAILS,
            on_detect=OnDetect.BLOCK,
            guardrail_id="gr-1",
        ),
        session=session,
    )
    redact = new_safety_check_from_config(
        SafetyConfig(
            kind=SafetyKind.BEDROCK_GUARDRAILS,
            on_detect=OnDetect.REDACT,
            guardrail_id="gr-1",
        ),
        session=session,
    )

    assert isinstance(block, BedrockGuardrailsSafetyCheck)
    blocked = await block.scan(_TEXT, kind=ScanKind.PROMPT)
    passed = await block.scan("hello", kind=ScanKind.PROMPT)
    masked = await redact.scan(_TEXT, kind=ScanKind.ANSWER)
    await block.aclose()

    assert (blocked.decision, blocked.flags) == (
        SafetyDecision.BLOCK,
        ("content.violence", "pii.email"),
    )
    assert (passed.decision, passed.flags) == (SafetyDecision.ALLOW, ())
    assert (masked.decision, masked.flags, masked.redacted) == (
        SafetyDecision.REDACT,
        ("guardrail.intervened",),
        "masked",
    )
    sources = [call.kwargs["source"] for call in client.apply_guardrail.await_args_list]
    assert sources == ["INPUT", "INPUT", "OUTPUT"]
    assert (
        client.apply_guardrail.await_args_list[0].kwargs["guardrailIdentifier"] == "gr-1"
    )


@pytest.mark.asyncio
async def test_managed_client_error_is_coded() -> None:
    """Test that an AWS failure crosses the safety boundary as a coded error.

    **Why this test is important:**
      - A raw botocore error must never reach the audit proxy; its enforcement mode decides
        what an outage means, and that decision reads the ``ErrorCode``.

    **What it tests:**
      - a throttled Comprehend call raises ``AppError(UNAVAILABLE)``
    """
    client = MagicMock()
    client.detect_pii_entities = AsyncMock(
        side_effect=ClientError(
            {
                "Error": {"Code": "ThrottlingException", "Message": "slow down"},
                "ResponseMetadata": {
                    "RequestId": "",
                    "HostId": "",
                    "HTTPStatusCode": 400,
                    "HTTPHeaders": {},
                    "RetryAttempts": 0,
                },
            },
            "DetectPiiEntities",
        )
    )
    check = new_safety_check_from_config(
        SafetyConfig(kind=SafetyKind.COMPREHEND), session=_session(client)
    )

    with pytest.raises(AppError) as caught:
        await check.scan(_TEXT, kind=ScanKind.PROMPT)

    assert caught.value.code is ErrorCode.UNAVAILABLE
