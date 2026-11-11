"""``BedrockSinkConfig``: where and how the Bedrock sink writes a data source's files."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from techai_webutils.clients.sink.bedrock.layout import BedrockLayout

if TYPE_CHECKING:
    from collections.abc import Mapping

DEFAULT_MAX_UNIT_BYTES = 25 << 20
"""The ``kb_units`` file ceiling (25 MiB), as the KB-unit splitter's default."""


def _no_tags() -> Mapping[str, str]:
    """Return an empty tag map.

    A plain ``dict``, not a ``MappingProxyType``: configs travel to Ray workers by pickle.

    Returns:
        An empty mapping.

    """
    return {}


@dataclass(frozen=True, slots=True)
class BedrockSinkConfig:
    """The data source's bucket and prefix, the file layout and the write concurrency."""

    bucket: str = ""
    """The data source's bucket (required by the ``bedrock`` kind)."""
    prefix: str = "kb"
    """Key prefix; each org's files are under ``{prefix}/{org}/``."""
    layout: BedrockLayout = BedrockLayout.CHUNK_FILES
    """``chunk_files`` for a ``NONE``-chunking data source, ``kb_units`` for
    ``FIXED_SIZE``."""
    max_unit_bytes: int = DEFAULT_MAX_UNIT_BYTES
    """Largest ``kb_units`` file; a single bigger chunk is written alone."""
    concurrency: int = 16
    """Most object-store calls one write, delete or update keeps in flight."""
    object_tags: Mapping[str, str] = field(default_factory=_no_tags)
    """Object tags put on every file and sidecar the sink writes, so a bucket lifecycle
    rule can select the sink's objects wherever their prefix lies (S3 filters take no
    wildcard). Empty writes untagged objects; the writer then needs
    ``s3:PutObjectTagging`` as well as ``s3:PutObject``."""
