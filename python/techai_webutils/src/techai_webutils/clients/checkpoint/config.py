"""Checkpoint store configuration: the Kind and the ``s3db`` layout settings."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum

DEFAULT_MAX_ARTIFACT_BYTES = 64 * 1024 * 1024
"""The largest artifact payload written as one object (64 MiB); a larger one is segmented."""


class CheckpointKind(StrEnum):
    """Which checkpoint store to build."""

    NOOP = "noop"
    """No checkpoints: every get misses and every write is dropped (today's behaviour)."""
    S3DB = "s3db"
    """Artifacts in an object store, stage records through the injected state client."""


class CheckpointCompression(StrEnum):
    """How the ``s3db`` store compresses what it writes (it reads either, whatever is set)."""

    GZIP = "gzip"
    """gzip from the standard library; needs no extra package."""
    ZSTD = "zstd"
    """Zstandard, far cheaper in CPU at a similar size."""


@dataclass(frozen=True)
class CheckpointConfig:
    """Selects the checkpoint store and, for ``s3db``, where and how artifacts are stored."""

    kind: CheckpointKind = CheckpointKind.NOOP
    """The store to build; ``noop`` by default."""
    bucket: str = ""
    """The bucket artifacts are stored in (``s3db``; required there)."""
    prefix: str = "checkpoints"
    """The key prefix every checkpoint object lives under."""
    part_prefix: str = "part"
    """The file-name prefix of a stage's parts."""
    max_artifact_bytes: int = DEFAULT_MAX_ARTIFACT_BYTES
    """Payloads larger than this are written as segments of at most this size."""
    compression: CheckpointCompression = CheckpointCompression.GZIP
    """How new objects are compressed (``s3db``); objects already stored stay readable."""
