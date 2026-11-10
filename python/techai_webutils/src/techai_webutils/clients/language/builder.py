"""``language_identifier_from_config``: the config-selected ``LanguageIdentifier``.

``langdetect`` (the default) is today's backend: the same library and seed as the document
metadata's language. ``fasttext`` (the ``lid.176`` model) and ``glotlid`` (GlotLID, for
long-tail languages) run a fastText model file from ``model_path``; ``lingua`` reads short
text well. ``fasttext`` becomes the default only when the consumer's parity test and
baseline check show no accuracy loss and no added cost. Each backend's library is imported
only when its kind is built (the ``langdetect``, ``fasttext`` and ``lingua`` extras).

Two decorators apply from the config: ``short_text_kind`` routes text under
    ``short_text_chars``
characters to its own backend (``ShortTextRouter``), and ``min_confidence`` reports
``und`` for a guess below it (``ConfidenceFloor``, outermost).
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.language.routing import ConfidenceFloor, ShortTextRouter
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.language.fasttext.identifier import (
        FastTextIdentifier,
        LabelScheme,
    )
    from techai_webutils.clients.language.langdetect.identifier import (
        LangdetectIdentifier,
    )
    from techai_webutils.clients.language.lingua.identifier import LinguaIdentifier
    from techai_webutils.core.interfaces.language_identifier import LanguageIdentifier


class LanguageIdentifierKind(StrEnum):
    """Which language identifier to build."""

    LANGDETECT = "langdetect"
    """langdetect, seeded (the default; ISO 639-1 codes)."""
    FASTTEXT = "fasttext"
    """A fastText ``lid.176`` model file (ISO 639-1 codes)."""
    GLOTLID = "glotlid"
    """A GlotLID fastText model file (ISO 639-3 codes and the script)."""
    LINGUA = "lingua"
    """Lingua, accurate on a few words (ISO 639-1 codes)."""


@dataclass(frozen=True, slots=True)
class LanguageIdentifierConfig:
    """Selects the language identifier and its model."""

    kind: LanguageIdentifierKind = LanguageIdentifierKind.LANGDETECT
    """The identifier to build; ``langdetect`` by default."""
    model_path: str = ""
    """``fasttext`` and ``glotlid``: the model file (required; loaded on first use)."""
    short_text_kind: LanguageIdentifierKind | None = None
    """The backend for text under ``short_text_chars`` characters (``lingua``); None
    routes none."""
    short_text_chars: int = 0
    """The length below which text goes to ``short_text_kind``."""
    min_confidence: float = 0.0
    """The lowest confidence reported as a language; a guess below it is ``und``."""


def language_identifier_from_config(
    config: LanguageIdentifierConfig,
) -> LanguageIdentifier:
    """Build the ``LanguageIdentifier`` ``config.kind`` selects.

    Args:
        config: Selects the backend and carries its settings.

    Returns:
        The ``LanguageIdentifier`` ``config.kind`` selects.

    Raises:
        AppValueError: An unknown kind, ``fasttext`` / ``glotlid`` without a model path, a
            ``short_text_kind`` without a positive ``short_text_chars``, or
            ``min_confidence`` outside ``[0, 1]`` (``INVALID_INPUT``).

    """
    if not 0.0 <= config.min_confidence <= 1.0:
        msg = f"min_confidence {config.min_confidence} is outside [0, 1]"
        raise AppValueError(msg)
    identifier = _backend(config.kind, config.model_path)
    if config.short_text_kind is not None:
        if config.short_text_chars < 1:
            msg = "a short_text_kind needs a positive short_text_chars"
            raise AppValueError(msg)
        short = _backend(config.short_text_kind, config.model_path)
        identifier = ShortTextRouter(
            identifier, short, short_chars=config.short_text_chars
        )
    if config.min_confidence > 0.0:
        identifier = ConfidenceFloor(identifier, min_confidence=config.min_confidence)
    return identifier


def _backend(kind: LanguageIdentifierKind, model_path: str) -> LanguageIdentifier:
    """Build the bare backend ``kind`` names, importing its library only now.

    Args:
        kind: The backend.
        model_path: The model file of a fastText backend.

    Returns:
        The backend.

    Raises:
        AppValueError: An unknown kind, or a fastText backend without a model path.

    """
    if kind == LanguageIdentifierKind.LINGUA:
        module = importlib.import_module(
            "techai_webutils.clients.language.lingua.identifier"
        )
        return cast("type[LinguaIdentifier]", module.LinguaIdentifier)()
    if kind == LanguageIdentifierKind.LANGDETECT:
        module = importlib.import_module(
            "techai_webutils.clients.language.langdetect.identifier"
        )
        return cast("type[LangdetectIdentifier]", module.LangdetectIdentifier)()
    if kind in {LanguageIdentifierKind.FASTTEXT, LanguageIdentifierKind.GLOTLID}:
        if not model_path:
            msg = f"the {kind} language identifier needs a model_path"
            raise AppValueError(msg)
        module = importlib.import_module(
            "techai_webutils.clients.language.fasttext.identifier"
        )
        identifier = cast("type[FastTextIdentifier]", module.FastTextIdentifier)
        schemes = cast("type[LabelScheme]", module.LabelScheme)
        scheme = (
            schemes.GLOTLID
            if kind == LanguageIdentifierKind.GLOTLID
            else schemes.ISO_639_1
        )
        return identifier(model_path, scheme=scheme)
    msg = f"unknown language identifier kind: {kind!r}"
    raise AppValueError(msg)
