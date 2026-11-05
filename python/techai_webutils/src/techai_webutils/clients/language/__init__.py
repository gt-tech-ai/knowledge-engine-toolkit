"""Language identifiers (``LanguageIdentifierKind``: ``langdetect``, ``fasttext``, ``glotlid``) and their factory."""

from techai_webutils.clients.language.builder import (
    LanguageIdentifierConfig,
    LanguageIdentifierKind,
    language_identifier_from_config,
)

__all__ = [
    "LanguageIdentifierConfig",
    "LanguageIdentifierKind",
    "language_identifier_from_config",
]
