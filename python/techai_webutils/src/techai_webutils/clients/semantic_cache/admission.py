"""Config-selected ``CacheAdmission``: which questions may be looked up in and stored to the cache.

``allow_all`` (the default) admits every question except one containing a tenant's extra phrase;
``rules`` also refuses the configured phrases (time-relative or about the asker: "latest",
"assigned to me"). Every phrase matches case-insensitively, on word boundaries, as literal text:
a phrase is escaped, never used as a pattern, so a tenant cannot supply a regular expression.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.semantic_cache import CacheAdmission

if TYPE_CHECKING:
    from collections.abc import Iterable, Sequence


class CacheAdmissionKind(StrEnum):
    """The admission backends."""

    ALLOW_ALL = "allow_all"
    """Admit every question (only a tenant's extra phrases refuse)."""
    RULES = "rules"
    """Also refuse the configured phrases."""


@dataclass(frozen=True, slots=True)
class CacheAdmissionConfig:
    """Cache admission configuration."""

    kind: str = CacheAdmissionKind.ALLOW_ALL
    """A ``CacheAdmissionKind`` value."""
    phrases: tuple[str, ...] = ()
    """The never-cache phrases the ``rules`` kind refuses."""


def _contains_any(question: str, phrases: Iterable[str]) -> bool:
    """Return whether ``question`` contains any non-blank phrase as whole words (literal match)."""
    for phrase in phrases:
        text = phrase.strip()
        if text and re.search(
            rf"(?<!\w){re.escape(text)}(?!\w)", question, flags=re.IGNORECASE
        ):
            return True
    return False


class AllowAllAdmission(CacheAdmission):
    """Admits every question that contains none of the caller's extra phrases."""

    @override
    def admits(self, query: str, extra_phrases: Sequence[str] = ()) -> bool:
        """Return ``False`` only when ``query`` contains an extra phrase."""
        return not _contains_any(query, extra_phrases)


class RulesAdmission(CacheAdmission):
    """Refuses questions containing a configured or an extra phrase."""

    def __init__(self, phrases: Sequence[str]) -> None:
        """Bind the configured never-cache phrases."""
        self._phrases = tuple(phrases)

    @override
    def admits(self, query: str, extra_phrases: Sequence[str] = ()) -> bool:
        """Return ``False`` when ``query`` contains a configured or an extra phrase."""
        return not _contains_any(query, (*self._phrases, *extra_phrases))


def cache_admission_from_config(config: CacheAdmissionConfig) -> CacheAdmission:
    """Build the admission ``config.kind`` selects.

    Raises:
        AppValueError: ``INVALID_INPUT`` for an unknown kind.

    """
    if config.kind == CacheAdmissionKind.ALLOW_ALL:
        return AllowAllAdmission()
    if config.kind == CacheAdmissionKind.RULES:
        return RulesAdmission(config.phrases)
    msg = f"unknown cache admission kind: {config.kind!r}"
    raise AppValueError(msg)


_INTERFACE_CHECK: tuple[type[CacheAdmission], ...] = (AllowAllAdmission, RulesAdmission)
"""Type-checker assertion that both kinds satisfy ``CacheAdmission``."""
