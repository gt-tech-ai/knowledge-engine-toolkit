"""Unit tests for semantic cache admission: which questions may use the cache."""

from __future__ import annotations

import pytest

from techai_webutils.clients.semantic_cache.admission import (
    AllowAllAdmission,
    CacheAdmissionConfig,
    CacheAdmissionKind,
    cache_admission_from_config,
)
from techai_webutils.core.errors import AppError, ErrorCode

_RULES = CacheAdmissionConfig(
    kind=CacheAdmissionKind.RULES, phrases=("latest", "assigned to me")
)


def test_allow_all_is_the_default_and_admits_everything() -> None:
    """Test that the default admission caches every question.

    **Why this test is important:**
      - The default must keep today's behaviour: only a configured rule removes a question.

    **What it tests:**
      - the factory's default is ``AllowAllAdmission`` and it admits "what is the latest release"
      - but it still honours a tenant's extra phrase
    """
    admission = cache_admission_from_config(CacheAdmissionConfig())

    assert isinstance(admission, AllowAllAdmission)
    assert admission.admits("what is the latest release")
    assert not admission.admits("who is on-call", extra_phrases=("on-call",))


@pytest.mark.parametrize(
    ("question", "extra", "expected"),
    [
        ("What's the LATEST release?", (), False),
        ("what is latestness", (), True),
        ("tickets assigned to me", (), False),
        ("who is on-call this week", ("on-call",), False),
        ("is axb valid", ("a.b",), True),
        ("is a.b valid", ("a.b",), False),
        ("the quarterly plan", ("",), True),
    ],
    ids=[
        "case-insensitive",
        "word-boundary",
        "phrase",
        "extra",
        "literal-dot",
        "dot",
        "blank",
    ],
)
def test_rules_refuse_configured_and_extra_phrases_literally(
    question: str, extra: tuple[str, ...], *, expected: bool
) -> None:
    """Test that rules match whole phrases, case-insensitively, as literal text.

    **Why this test is important:**
      - Time- and user-specific answers must never be shared; and a tenant's phrase is text,
        never a pattern, so "a.b" cannot match "axb" and a phrase cannot be a regex attack.

    **What it tests:**
      - configured and extra phrases refuse on a word-boundary, case-insensitive match
      - "latestness" is admitted; a literal dot matches only a dot; a blank phrase is ignored
    """
    admission = cache_admission_from_config(_RULES)

    assert admission.admits(question, extra_phrases=extra) is expected


def test_admission_factory_refuses_an_unknown_kind() -> None:
    """Test that an unknown admission kind fails loudly.

    **Why this test is important:**
      - A typo must stop startup rather than silently cache time-specific answers.

    **What it tests:**
      - kind ``"maybe"`` raises ``INVALID_INPUT``
    """
    with pytest.raises(AppError) as caught:
        cache_admission_from_config(CacheAdmissionConfig(kind="maybe"))
    assert caught.value.code is ErrorCode.INVALID_INPUT
