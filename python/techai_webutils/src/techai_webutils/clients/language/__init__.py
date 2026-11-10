"""Language identifiers, their routing decorators and their factory.

The kinds (``LanguageIdentifierKind``) are ``langdetect``, ``fasttext``, ``glotlid`` and
``lingua``.
"""

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
