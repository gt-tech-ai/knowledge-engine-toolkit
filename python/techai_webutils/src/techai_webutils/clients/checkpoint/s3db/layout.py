"""Object keys and the artifact envelope of the ``s3db`` checkpoint store.

Under ``prefix``, with ``hash2`` the content hash's first two characters (spreading keys over S3
partitions) and ``fp12`` the fingerprint's first twelve:

- artifact ``{prefix}/{hash2}/{org}/{content_hash}/{stage}-{fp12}.json.gz``;
- its segments (a payload over the size cap) ``….seg-{n}.gz``;
- its parts ``…/{stage}-{fp12}/{part_prefix}-{n}.json.gz``;
- its holders ``…/{stage}-{fp12}.refs/{document_id}`` (empty), one per document using it;
- a document's references ``{prefix}/refs/{org}/{document_id}/{content_hash}-{stage}-{fp12}``
  (empty), listing what it holds.

The envelope is gzip of one JSON header line followed by the raw payload, so a payload is never
re-encoded.
"""

from __future__ import annotations

import gzip
import json
from dataclasses import dataclass
from typing import TYPE_CHECKING, cast

from techai_webutils.core.interfaces.checkpoint import CheckpointKey, Stage

if TYPE_CHECKING:
    from collections.abc import Mapping

FORMAT_VERSION = 1
"""The envelope version this release writes; it reads this one and the one before."""


@dataclass(frozen=True)
class Layout:
    """Builds the store's object keys under one prefix."""

    prefix: str
    """The key prefix every object lives under."""
    part_prefix: str
    """The file-name prefix of a part."""

    def base(self, key: CheckpointKey) -> str:
        """Return the artifact's key stem, shared by its envelope, segments, parts and holders."""
        hash2 = key.content_hash[:2]
        return (
            f"{self.prefix}/{hash2}/{key.org_id}/{key.content_hash}/"
            f"{key.stage}-{key.fingerprint[:12]}"
        )

    def artifact(self, key: CheckpointKey) -> str:
        """Return the envelope's key."""
        return f"{self.base(key)}.json.gz"

    def segment(self, key: CheckpointKey, n: int) -> str:
        """Return segment ``n``'s key."""
        return f"{self.base(key)}.seg-{n}.gz"

    def part(self, key: CheckpointKey, n: int) -> str:
        """Return part ``n``'s key."""
        return f"{self.base(key)}/{self.part_prefix}-{n}.json.gz"

    def parts_prefix(self, key: CheckpointKey) -> str:
        """Return the prefix every part of the artifact shares."""
        return f"{self.base(key)}/{self.part_prefix}-"

    def holders(self, key: CheckpointKey) -> str:
        """Return the prefix of the artifact's holder markers."""
        return f"{self.base(key)}.refs/"

    def references(self, org_id: str, document_id: str) -> str:
        """Return the prefix of a document's reference markers."""
        return f"{self.prefix}/refs/{org_id}/{document_id}/"

    def reference(self, key: CheckpointKey) -> str:
        """Return the document's reference marker for the artifact."""
        name = f"{key.content_hash}-{key.stage}-{key.fingerprint[:12]}"
        return f"{self.references(key.org_id, key.document_id)}{name}"


def key_of(reference: str, org_id: str, document_id: str) -> CheckpointKey:
    """Recover the checkpoint key (with a 12-character fingerprint) a reference marker names."""
    content_hash, stage, fp12 = reference.rsplit("/", 1)[-1].rsplit("-", 2)
    return CheckpointKey(org_id, document_id, content_hash, Stage(stage), fp12)


def encode(header: Mapping[str, object], payload: bytes) -> bytes:
    """Return the gzip envelope of ``header`` and ``payload``."""
    line = json.dumps(header, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return gzip.compress(line + b"\n" + payload)


def decode(body: bytes) -> tuple[dict[str, object], bytes]:
    """Split an envelope into its header and payload.

    Raises:
        ValueError: The body has no header line, or the header is not JSON.
        TypeError: The header is not a JSON object.
        OSError: The gzip stream is corrupt.

    """
    raw = gzip.decompress(body)
    line, sep, payload = raw.partition(b"\n")
    if not sep:
        msg = "checkpoint envelope has no header line"
        raise ValueError(msg)
    header = json.loads(line)
    if not isinstance(header, dict):
        msg = "checkpoint envelope header is not an object"
        raise TypeError(msg)
    fields = cast("dict[object, object]", header)
    return {str(k): v for k, v in fields.items()}, payload
