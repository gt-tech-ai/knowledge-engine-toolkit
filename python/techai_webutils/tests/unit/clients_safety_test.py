"""Unit tests for the config-selected ``SafetyCheck`` factory.

And the ``noop`` / ``regex`` kinds.
"""

from __future__ import annotations

import asyncio

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.clients.safety import (
    OnDetect,
    SafetyConfig,
    SafetyKind,
    new_safety_check_from_config,
)
from techai_webutils.clients.safety.noop import NoopSafetyCheck
from techai_webutils.clients.safety.regex import RegexPiiSafetyCheck
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.safety import SafetyDecision, SafetyResult, ScanKind
from tests.child_python import run_python

_EXPECTED_COUNT = 2

_TEXT = "SSN 123-45-6789, mail jane.doe@example.com, call (555) 123-4567 today"

_SSN = st.builds(
    "{:03d}-{:02d}-{:04d}".format,
    st.integers(100, 899),
    st.integers(10, 99),
    st.integers(1000, 9999),
)
_EMAIL = st.from_regex(r"[a-z0-9]{1,10}@[a-z]{1,10}\.[a-z]{2,4}", fullmatch=True)
_PHONE = st.builds(
    "({:03d}) {:03d}-{:04d}".format,
    st.integers(200, 999),
    st.integers(200, 999),
    st.integers(0, 9999),
)
_FILLER = st.text(
    alphabet=st.characters(blacklist_categories=("Nd", "Cs"), blacklist_characters="@"),
    max_size=40,
)


@pytest.mark.asyncio
async def test_regex_flags_ssn_email_phone() -> None:
    """Test that the regex check flags each PII type it knows.

    **Why this test is important:**
      - The regex kind is the deterministic, infra-free detector CI and dev rely on; a
        missed shape would let PII into the audit trail unflagged.

    **What it tests:**
      - text with an SSN, an email and a phone number yields exactly ``("pii.ssn",
        "pii.email", "pii.phone")``
      - with ``on_detect=flag`` the decision is ``allow`` and nothing is redacted
    """
    check = RegexPiiSafetyCheck(OnDetect.FLAG)

    result = await check.scan(_TEXT, kind=ScanKind.PROMPT)

    assert result == SafetyResult(
        flags=("pii.ssn", "pii.email", "pii.phone"),
        decision=SafetyDecision.ALLOW,
        redacted=None,
    )


@given(pii=st.one_of(_SSN, _EMAIL, _PHONE), before=_FILLER, after=_FILLER)
def test_regex_pbt_over_pii_shapes(pii: str, before: str, after: str) -> None:
    """Test that generated PII is flagged wherever it sits, and clean text never is.

    **Why this test is important:**
      - A detector is the sanctioned property-test case: hand-picked examples miss the
        shapes that slip through; a false positive on clean text would block real answers.

    **What it tests:**
      - any generated SSN / email / phone between arbitrary filler is flagged with its
        type
      - filler with no digits and no ``@`` is never flagged

    Args:
        pii: The PII.
        before: The text before it.
        after: The text after the PII.

    """
    check = RegexPiiSafetyCheck(OnDetect.FLAG)
    expected = (
        "pii.email"
        if "@" in pii
        else "pii.ssn"
        if pii.count("-") == _EXPECTED_COUNT
        else "pii.phone"
    )

    flagged = asyncio.run(check.scan(f"{before} {pii} {after}", kind=ScanKind.ANSWER))
    clean = asyncio.run(check.scan(before + after, kind=ScanKind.ANSWER))

    assert expected in flagged.flags
    assert clean.flags == ()


@pytest.mark.asyncio
async def test_regex_redact_rewrites_matches() -> None:
    """Test that the redact and block policies act on a detection.

    **Why this test is important:**
      - ``redact`` is how PII is kept out of a stored answer while still serving it;
        ``block`` is how a policy refuses it outright. Each must act only when something
        was found.

    **What it tests:**
      - ``on_detect=redact`` → decision ``redact`` with each match replaced by
        ``[REDACTED:<type>]``
      - ``on_detect=block`` → decision ``block``, nothing redacted
      - clean text → decision ``allow`` under every policy
    """
    redact = RegexPiiSafetyCheck(OnDetect.REDACT)
    block = RegexPiiSafetyCheck(OnDetect.BLOCK)

    redacted = await redact.scan(_TEXT, kind=ScanKind.ANSWER)
    blocked = await block.scan(_TEXT, kind=ScanKind.PROMPT)
    clean = await block.scan("nothing to see", kind=ScanKind.PROMPT)

    assert redacted.decision is SafetyDecision.REDACT
    assert redacted.redacted == (
        "SSN [REDACTED:ssn], mail [REDACTED:email], call [REDACTED:phone] today"
    )
    assert (blocked.decision, blocked.redacted) == (SafetyDecision.BLOCK, None)
    assert clean == SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)


@pytest.mark.asyncio
async def test_noop_passes_everything_flags_nothing() -> None:
    """Test that the noop check, the default, passes everything.

    **Why this test is important:**
      - The noop kind is the base-config default; it must be exactly today's behaviour so
        wiring the safety tier changes nothing until a detector is selected.

    **What it tests:**
      - the default config builds a ``NoopSafetyCheck``
      - it returns ``allow`` with no flags even for PII, and works as an async context
        manager
    """
    check = new_safety_check_from_config(SafetyConfig())

    async with check as entered:
        result = await entered.scan(_TEXT, kind=ScanKind.PROMPT)

    assert isinstance(check, NoopSafetyCheck)
    assert result == SafetyResult(flags=(), decision=SafetyDecision.ALLOW, redacted=None)


def test_factory_fails_loudly_on_unknown_kind() -> None:
    """Test that a misconfigured safety tier fails at build time.

    **Why this test is important:**
      - A typo in the safety kind or policy must stop boot, never silently fall back to
        the noop check and let unscanned text through.

    **What it tests:**
      - an unknown kind and an unknown ``on_detect`` each raise a ``ValueError`` coded
        ``INVALID_INPUT``
      - ``bedrock_guardrails`` without a guardrail id, and a managed kind with a
        non-positive ``timeout_seconds`` (every scan would time out), raise the same
      - ``regex`` builds a ``RegexPiiSafetyCheck``
    """
    bad = (
        SafetyConfig(kind="nope"),
        SafetyConfig(kind=SafetyKind.REGEX, on_detect="shout"),
        SafetyConfig(kind=SafetyKind.BEDROCK_GUARDRAILS),
        SafetyConfig(kind=SafetyKind.COMPREHEND, timeout_seconds=0),
        SafetyConfig(
            kind=SafetyKind.BEDROCK_GUARDRAILS, guardrail_id="gr-1", timeout_seconds=-1
        ),
    )
    for config in bad:
        with pytest.raises(AppValueError, match="safety") as caught:
            _ = new_safety_check_from_config(config)
        assert isinstance(caught.value, AppError)
        assert caught.value.code is ErrorCode.INVALID_INPUT

    regex = new_safety_check_from_config(SafetyConfig(kind=SafetyKind.REGEX))
    assert isinstance(regex, RegexPiiSafetyCheck)


def test_managed_backend_lazy_import() -> None:
    """Test that the infra-free kinds never load the AWS SDK.

    **Why this test is important:**
      - Dev and CI select ``noop`` / ``regex``; importing aiobotocore there would add
        seconds of start-up and an SDK the deployable may not ship.

    **What it tests:**
      - in a fresh interpreter, importing ``techai_webutils.clients.safety`` and building
        and running the ``regex`` kind leaves ``aiobotocore`` out of ``sys.modules``
    """
    script = (
        "import asyncio, sys\n"
        "from techai_webutils.clients.safety import SafetyConfig, "
        "new_safety_check_from_config\n"
        "from techai_webutils.core.interfaces.safety import ScanKind\n"
        "check = new_safety_check_from_config(SafetyConfig(kind='regex'))\n"
        "asyncio.run(check.scan('a@b.co', kind=ScanKind.PROMPT))\n"
        "assert 'aiobotocore' not in sys.modules, 'aiobotocore was imported'\n"
        "print('LAZY_OK')\n"
    )
    result = run_python(script)

    assert result.returncode == 0, result.stderr
    assert "LAZY_OK" in result.stdout
