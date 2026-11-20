"""Object layout of the Bedrock sink: keys, KB units and ``.metadata.json`` sidecars.

A document's objects live under ``{prefix}/{org}/{hash2}/{document_id}/{version}/``, with
``hash2`` the first two hex characters of the document id's SHA-256 (spreading keys over
S3 partitions) and ``version`` the first 16 characters of its content hash. Each unit is
``{n}.txt`` beside ``{n}.txt.metadata.json``, the sidecar Bedrock reads its filterable
attributes from (``{"metadataAttributes": {...}}``). The org leads, so one org's data
source can include only its own prefix; the version directory makes a changed document a
new set of keys, so a redelivery skips every key it finds and a new version replaces the
old one. A ``kb_units`` version also holds ``manifest.json`` (``MANIFEST_NAME``), written
after every unit, which names the unit count a reader waits for.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.sink import CHUNK_ID_KEY, chunk_id

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument

SIDECAR_SUFFIX = ".metadata.json"
"""The suffix Bedrock looks for beside each object for its metadata attributes."""

MANIFEST_NAME = "manifest.json"
"""The ``kb_units`` version file naming its content hash and unit count, written last."""

PAGE_FIELDS = ("page_start", "page_end")
"""Chunk metadata fields a ``kb_units`` sidecar carries as numbers when present."""

_VERSION_CHARS = 16
"""Characters of the content hash that name a version directory."""


class BedrockLayout(StrEnum):
    """How a document's chunks become files in the data source."""

    CHUNK_FILES = "chunk_files"
    """One file per chunk, for a data source with ``NONE`` chunking (Bedrock embeds each
    file as one chunk, so retrieval sees the engine's chunks)."""
    KB_UNITS = "kb_units"
    """One file per chunk (at most ``max_unit_bytes``) with its page range, plus a
    ``manifest.json`` written last, for a ``FIXED_SIZE`` data source that chunks each
    file itself."""


@dataclass(frozen=True, slots=True)
class Unit:
    """One file to write: its text and the attributes of its sidecar."""

    text: str
    """The file's body."""
    attributes: dict[str, object]
    """The sidecar's ``metadataAttributes``."""


def document_prefix(prefix: str, org_id: str, document_id: str) -> str:
    """Return the key prefix every object of the document lives under (ends with ``/``).

    Args:
        prefix: The sink's key prefix.
        org_id: The document's organization.
        document_id: The document.

    Returns:
        The key prefix every object of the document lives under (ends with ``/``).

    Raises:
        AppValueError: An empty org or document id, or one containing ``/``.

    """
    for name, value in (("org", org_id), ("document id", document_id)):
        if not value or "/" in value:
            msg = f"bedrock sink: {name} {value!r} must be non-empty and contain no '/'"
            raise AppValueError(msg)
    hash2 = hashlib.sha256(document_id.encode()).hexdigest()[:2]
    return f"{prefix}/{org_id}/{hash2}/{document_id}/"


def unit_prefix(prefix: str, org: str, document_id: str, content_hash: str) -> str:
    """Return the version directory a document's units and manifest live in.

    ``{prefix}/{org}/{hash2}/{document_id}/{content_hash[:16]}/``, so a reader lists one
    version without re-deriving ``hash2``. An org or document id ``document_prefix``
    refuses fails the same way here.

    Args:
        prefix: The sink's key prefix.
        org: The document's organization.
        document_id: The document.
        content_hash: The document version's content hash.

    Returns:
        The version's key prefix (ends with ``/``).

    """
    return f"{document_prefix(prefix, org, document_id)}{content_hash[:_VERSION_CHARS]}/"


def manifest(content_hash: str, unit_count: int) -> bytes:
    """Return a ``kb_units`` manifest body: the version's content hash and unit count.

    Args:
        content_hash: The document version's content hash.
        unit_count: How many units the version holds.

    Returns:
        The manifest's JSON body, keys sorted.

    """
    return json.dumps(
        {"content_hash": content_hash, "unit_count": unit_count},
        sort_keys=True,
        separators=(",", ":"),
    ).encode()


def unit_key(doc_prefix: str, content_hash: str, n: int) -> str:
    """Return the key of unit ``n`` of the document's ``content_hash`` version.

    Args:
        doc_prefix: The document's key prefix.
        content_hash: The document version's content hash.
        n: The unit's index.

    Returns:
        The key of unit ``n`` of the document's ``content_hash`` version.

    """
    return f"{doc_prefix}{content_hash[:_VERSION_CHARS]}/{n}.txt"


def sidecar(attributes: dict[str, object]) -> bytes:
    """Return a sidecar body: ``{"metadataAttributes": attributes}``, keys sorted.

    Args:
        attributes: The unit's metadata attributes.

    Returns:
        A sidecar body: ``{"metadataAttributes": attributes}``, keys sorted.

    """
    return json.dumps(
        {"metadataAttributes": attributes}, sort_keys=True, separators=(",", ":")
    ).encode()


def units(
    document: SinkDocument,
    chunks: Sequence[ChunkPayload],
    *,
    layout: BedrockLayout | str,
    org_field: str,
    max_unit_bytes: int,
) -> list[Unit]:
    """Return the files ``layout`` makes of the chunks, in order.

    Args:
        document: The document.
        chunks: Its chunks.
        layout: Whether a unit carries the chunk's own attributes (``chunk_files``) or
            the document's and its page range (``kb_units``).
        org_field: The attribute that carries the organization.
        max_unit_bytes: The largest ``kb_units`` file.

    Returns:
        The files ``layout`` makes of the chunks, one per chunk, in order.

    Raises:
        AppValueError: An unknown layout, a ``kb_units`` chunk over ``max_unit_bytes``,
            or a page field that is not a number (``INVALID_INPUT``).

    """
    if layout == BedrockLayout.CHUNK_FILES:
        return [
            Unit(
                chunk.text,
                _chunk_attributes(
                    document,
                    chunk,
                    n if chunk.ordinal is None else chunk.ordinal,
                    org_field,
                ),
            )
            for n, chunk in enumerate(chunks)
        ]
    if layout == BedrockLayout.KB_UNITS:
        return [
            _kb_unit(document, chunk, n, org_field, max_unit_bytes)
            for n, chunk in enumerate(chunks)
        ]
    msg = f"unknown bedrock layout: {layout!r}"
    raise AppValueError(msg)


def _kb_unit(
    document: SinkDocument,
    chunk: ChunkPayload,
    n: int,
    org_field: str,
    max_bytes: int,
) -> Unit:
    """Return chunk ``n`` as a ``kb_units`` file.

    Its sidecar holds the document's attributes, the chunk's own metadata (never
    over a reserved key: see ``_document_attributes``), ``chunk_index`` and the chunk's
    page range (``PAGE_FIELDS``) as numbers when the chunk has one.

    Args:
        document: The document.
        chunk: The chunk.
        n: The chunk's index in the write.
        org_field: The attribute that carries the organization.
        max_bytes: The largest file.

    Returns:
        The unit.

    Raises:
        AppValueError: The chunk is over ``max_bytes``, or a page field is not a number
            (``INVALID_INPUT``).

    """
    size = len(chunk.text.encode())
    if size > max_bytes:
        msg = (
            f"bedrock sink: chunk {n} of {document.document_id} is {size} bytes, over "
            f"max_unit_bytes {max_bytes}"
        )
        raise AppValueError(msg)
    attributes: dict[str, object] = {
        **_document_attributes(document, org_field, chunk),
        "chunk_index": n,
    }
    for key in PAGE_FIELDS:
        value = chunk.metadata.get(key)
        if value is None:
            continue
        if not value.isdecimal():
            msg = f"bedrock sink: chunk {n} {key} {value!r} is not a page number"
            raise AppValueError(msg)
        attributes[key] = int(value)
    return Unit(chunk.text, attributes)


def _document_attributes(
    document: SinkDocument, org_field: str, chunk: ChunkPayload
) -> dict[str, object]:
    """Return the document's and the chunk's attributes, the reserved keys last.

    The document's metadata, then the chunk's, then the reserved keys: the
    organization, ``document_id`` and ``content_hash``. A metadata field of a reserved
    key's name never replaces it, because those keys scope and cite every passage.

    Args:
        document: The document.
        org_field: The attribute that carries the organization.
        chunk: The chunk whose own metadata the file carries.

    Returns:
        The document's and the chunk's attributes, the reserved keys last.

    """
    return {
        **document.metadata,
        **chunk.metadata,
        org_field: document.org_id,
        "document_id": document.document_id,
        "content_hash": document.content_hash,
    }


def _chunk_attributes(
    document: SinkDocument, chunk: ChunkPayload, n: int, org_field: str
) -> dict[str, object]:
    """Return chunk ``n``'s attributes.

    The document's, the chunk's own (never over a reserved key: see
    ``_document_attributes``), and its structure.

    ``n`` is the chunk's ordinal in the document (its position in the write when unset).

    The chunk's id (``chunk_id``, as the vector sink ids it) is stored so a Bedrock
    passage cites the same chunk an OpenSearch one would; a ``kb_units`` file has none
    (Bedrock chunks it). Entities become string lists (``entities.<label>``), which
    Bedrock filters with ``listContains``.

    Args:
        document: The document.
        chunk: The chunk.
        n: The unit's index.
        org_field: The attribute that carries the organization.

    Returns:
        Chunk ``n``'s attributes: the document's, the chunk's own.

    """
    attributes: dict[str, object] = {
        **_document_attributes(document, org_field, chunk),
        "chunk_index": n,
        CHUNK_ID_KEY: chunk_id(document.document_id, n),
    }
    for key in ("section_path", "element_type", "language"):
        if value := cast("object", getattr(chunk, key)):
            attributes[key] = str(value)
    for label, values in chunk.entities.items():
        if values:
            attributes[f"entities.{label}"] = list(values)
    return attributes
