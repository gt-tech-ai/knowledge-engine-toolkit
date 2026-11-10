"""Env-aware metadata-extractor factory (foundation/logger ``NewFromConfig`` pattern).

Selects the ``metadata`` backend from ``MetadataConfig.kind``. Only ``langdetect`` ships
today; a richer NLP extractor would be a future ``Kind``. Unknown kinds fail loudly,
matching the Go ``NewFromConfig`` contract. The heavy ``langdetect`` dep stays lazy — its
backend subpackage is imported only when the ``langdetect`` kind is selected.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.clients.metadata.langdetect import (
        LangdetectMetadataExtractor as _LazyLangdetectMetadataExtractor,
    )
    from techai_webutils.core.interfaces.metadata import MetadataExtractor


class MetadataKind(StrEnum):
    """Which metadata-extractor backend to build."""

    LANGDETECT = "langdetect"
    """Format/counts + langdetect language detection (the only backend today)."""


@dataclass(frozen=True, slots=True)
class MetadataConfig:
    """Metadata-extractor configuration.

    (the consumer maps its own config section onto it).
    """

    kind: MetadataKind = MetadataKind.LANGDETECT
    """Selects the metadata backend (only ``langdetect`` ships today)."""


def metadata_extractor_from_config(config: MetadataConfig) -> MetadataExtractor:
    """Build the ``MetadataExtractor`` selected by ``config.kind`` (langdetect).

    Unknown kinds fail loudly.

    The ``langdetect`` backend subpackage (and its ``langdetect`` import + profile warmup)
    is loaded lazily here, so a deployable that never extracts metadata never pays the
    import.

    Args:
        config: Selects the backend and carries its settings.

    Returns:
        The ``MetadataExtractor`` selected by ``config.kind`` (langdetect).

    Raises:
        AppValueError: Unknown metadata extractor kind: ``config.kind``.

    """
    if config.kind is MetadataKind.LANGDETECT:
        langdetect_metadata_extractor_cls = cast(
            "type[_LazyLangdetectMetadataExtractor]",
            importlib.import_module(
                "techai_webutils.clients.metadata.langdetect"
            ).LangdetectMetadataExtractor,
        )

        return langdetect_metadata_extractor_cls()
    msg = f"unknown metadata extractor kind: {config.kind!r}"
    raise AppValueError(msg)
