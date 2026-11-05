"""``language_identifier_from_config``: the config-selected ``LanguageIdentifier``.

``langdetect`` (the default) is today's backend: the same library and seed as the document
metadata's language. ``fasttext`` (the ``lid.176`` model) and ``glotlid`` (GlotLID, for long-tail
languages) run a fastText model file from ``model_path``. ``fasttext`` becomes the default only
when the consumer's parity test and baseline check show no accuracy loss and no added cost. Each
backend's library is imported only when its kind is built (the ``langdetect`` and ``fasttext``
extras).
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.language.fasttext.identifier import FastTextIdentifier
    from techai_webutils.clients.language.langdetect.identifier import (
        LangdetectIdentifier,
    )
    from techai_webutils.core.interfaces.language_identifier import LanguageIdentifier


class LanguageIdentifierKind(StrEnum):
    """Which language identifier to build."""

    LANGDETECT = "langdetect"
    """langdetect, seeded (the default; ISO 639-1 codes)."""
    FASTTEXT = "fasttext"
    """A fastText ``lid.176`` model file (ISO 639-1 codes)."""
    GLOTLID = "glotlid"
    """A GlotLID fastText model file (ISO 639-3 codes and the script)."""


@dataclass(frozen=True, slots=True)
class LanguageIdentifierConfig:
    """Selects the language identifier and its model."""

    kind: LanguageIdentifierKind = LanguageIdentifierKind.LANGDETECT
    """The identifier to build; ``langdetect`` by default."""
    model_path: str = ""
    """``fasttext`` and ``glotlid``: the model file (required; loaded on first use)."""


def language_identifier_from_config(
    config: LanguageIdentifierConfig,
) -> LanguageIdentifier:
    """Build the ``LanguageIdentifier`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, or ``fasttext`` / ``glotlid`` without a model path
            (``INVALID_INPUT``).

    """
    if config.kind == LanguageIdentifierKind.LANGDETECT:
        module = importlib.import_module(
            "techai_webutils.clients.language.langdetect.identifier"
        )
        return cast("type[LangdetectIdentifier]", module.LangdetectIdentifier)()
    if config.kind in {LanguageIdentifierKind.FASTTEXT, LanguageIdentifierKind.GLOTLID}:
        if not config.model_path:
            msg = f"the {config.kind} language identifier needs a model_path"
            raise AppValueError(msg)
        module = importlib.import_module(
            "techai_webutils.clients.language.fasttext.identifier"
        )
        identifier = cast("type[FastTextIdentifier]", module.FastTextIdentifier)
        scheme = (
            module.LabelScheme.GLOTLID
            if config.kind == LanguageIdentifierKind.GLOTLID
            else module.LabelScheme.ISO_639_1
        )
        return identifier(config.model_path, scheme=scheme)
    msg = f"unknown language identifier kind: {config.kind!r}"
    raise AppValueError(msg)
