"""Document-body readers (kinds ``stub``, ``s3``, ``github``) and their factory."""

from techai_webutils.clients.source_reader.builder import source_reader_from_config
from techai_webutils.clients.source_reader.config import (
    SourceReaderConfig,
    SourceReaderKind,
)

__all__ = [
    "SourceReaderConfig",
    "SourceReaderKind",
    "source_reader_from_config",
]
