"""``BedrockSinkConfig``: where and how the Bedrock sink writes a data source's files."""

from __future__ import annotations

from dataclasses import dataclass

from techai_webutils.clients.sink.bedrock.layout import BedrockLayout

DEFAULT_MAX_UNIT_BYTES = 25 << 20
"""The ``kb_units`` file ceiling (25 MiB), as the KB-unit splitter's default."""


@dataclass(frozen=True, slots=True)
class BedrockSinkConfig:
    """The data source's bucket and prefix, the file layout and the write concurrency."""

    bucket: str = ""
    """The data source's bucket (required by the ``bedrock`` kind)."""
    prefix: str = "kb"
    """Key prefix; each org's files are under ``{prefix}/{org}/``."""
    layout: BedrockLayout = BedrockLayout.CHUNK_FILES
    """``chunk_files`` for a ``NONE``-chunking data source, ``kb_units`` for ``FIXED_SIZE``."""
    max_unit_bytes: int = DEFAULT_MAX_UNIT_BYTES
    """Largest ``kb_units`` file; a single bigger chunk is written alone."""
    concurrency: int = 16
    """Most object-store calls one write, delete or update keeps in flight."""
