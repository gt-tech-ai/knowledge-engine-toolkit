"""Language identifiers (``LanguageIdentifierKind``: ``langdetect``, ``fasttext``, ``glotlid``, ``lingua``), their routing decorators and their factory."""

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
