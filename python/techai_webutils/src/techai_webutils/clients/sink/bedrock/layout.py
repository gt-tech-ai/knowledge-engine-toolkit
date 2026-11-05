"""Object layout of the Bedrock sink: keys, KB units and ``.metadata.json`` sidecars.

A document's objects live under ``{prefix}/{org}/{hash2}/{document_id}/{version}/``, with
``hash2`` the first two hex characters of the document id's SHA-256 (spreading keys over S3
partitions) and ``version`` the first 16 characters of its content hash. Each unit is
``{n}.txt`` beside ``{n}.txt.metadata.json``, the sidecar Bedrock reads its filterable
attributes from (``{"metadataAttributes": {...}}``). The org leads, so one org's data source
can include only its own prefix; the version directory makes a changed document a new set of
keys, so a redelivery skips every key it finds and a new version replaces the old one.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument

SIDECAR_SUFFIX = ".metadata.json"
"""The suffix Bedrock looks for beside each object for its metadata attributes."""

UNIT_SEPARATOR = "\n\n"
"""Joins the chunks packed into one KB unit."""

_VERSION_CHARS = 16
"""Characters of the content hash that name a version directory."""


class BedrockLayout(StrEnum):
    """How a document's chunks become files in the data source."""

    CHUNK_FILES = "chunk_files"
    """One file per chunk, for a data source with ``NONE`` chunking (Bedrock embeds each file
    as one chunk, so retrieval sees the engine's chunks)."""
    KB_UNITS = "kb_units"
    """Contiguous chunks packed into files of at most ``max_unit_bytes``, for a ``FIXED_SIZE``
    data source that chunks them itself."""


@dataclass(frozen=True, slots=True)
class Unit:
    """One file to write: its text and the attributes of its sidecar."""

    text: str
    """The file's body."""
    attributes: dict[str, object]
    """The sidecar's ``metadataAttributes``."""


def document_prefix(prefix: str, org_id: str, document_id: str) -> str:
    """Return the key prefix every object of the document lives under (ends with ``/``).

    Raises:
        AppValueError: An empty org or document id, or one containing ``/``.

    """
    for name, value in (("org", org_id), ("document id", document_id)):
        if not value or "/" in value:
            msg = f"bedrock sink: {name} {value!r} must be non-empty and contain no '/'"
            raise AppValueError(msg)
    hash2 = hashlib.sha256(document_id.encode()).hexdigest()[:2]
    return f"{prefix}/{org_id}/{hash2}/{document_id}/"


def unit_key(doc_prefix: str, content_hash: str, n: int) -> str:
    """Return the key of unit ``n`` of the document's ``content_hash`` version."""
    return f"{doc_prefix}{content_hash[:_VERSION_CHARS]}/{n}.txt"


def sidecar(attributes: dict[str, object]) -> bytes:
    """Return a sidecar body: ``{"metadataAttributes": attributes}``, keys sorted."""
    return json.dumps(
        {"metadataAttributes": attributes}, sort_keys=True, separators=(",", ":")
    ).encode()


def units(
    document: SinkDocument,
    chunks: Sequence[ChunkPayload],
    *,
    layout: BedrockLayout,
    org_field: str,
    max_unit_bytes: int,
) -> list[Unit]:
    """Return the files ``layout`` makes of the chunks, in order.

    Raises:
        AppValueError: An unknown layout (``INVALID_INPUT``).

    """
    if layout == BedrockLayout.CHUNK_FILES:
        return [
            Unit(chunk.text, _chunk_attributes(document, chunk, n, org_field))
            for n, chunk in enumerate(chunks)
        ]
    if layout == BedrockLayout.KB_UNITS:
        return [
            Unit(text, {**_document_attributes(document, org_field), "chunk_index": n})
            for n, text in enumerate(_pack(chunks, max_unit_bytes))
        ]
    msg = f"unknown bedrock layout: {layout!r}"
    raise AppValueError(msg)


def _pack(chunks: Sequence[ChunkPayload], max_bytes: int) -> list[str]:
    """Pack contiguous chunk texts into units of at most ``max_bytes`` (a bigger chunk alone)."""
    packed: list[str] = []
    current: list[str] = []
    size = 0
    for chunk in chunks:
        length = len(chunk.text.encode())
        if current and size + len(UNIT_SEPARATOR) + length > max_bytes:
            packed.append(UNIT_SEPARATOR.join(current))
            current, size = [], 0
        size += length + (len(UNIT_SEPARATOR) if current else 0)
        current.append(chunk.text)
    if current:
        packed.append(UNIT_SEPARATOR.join(current))
    return packed


def _document_attributes(document: SinkDocument, org_field: str) -> dict[str, object]:
    """Return the attributes every file of the document carries."""
    return {
        **document.metadata,
        org_field: document.org_id,
        "document_id": document.document_id,
        "content_hash": document.content_hash,
    }


def _chunk_attributes(
    document: SinkDocument, chunk: ChunkPayload, n: int, org_field: str
) -> dict[str, object]:
    """Return chunk ``n``'s attributes: the document's, the chunk's own, and its structure.

    Entities become string lists (``entities.<label>``), which Bedrock filters with
    ``listContains``.
    """
    attributes: dict[str, object] = {
        **_document_attributes(document, org_field),
        **chunk.metadata,
        "chunk_index": n,
    }
    for key in ("section_path", "element_type", "language"):
        if value := getattr(chunk, key):
            attributes[key] = str(value)
    for label, values in chunk.entities.items():
        if values:
            attributes[f"entities.{label}"] = list(values)
    return attributes
