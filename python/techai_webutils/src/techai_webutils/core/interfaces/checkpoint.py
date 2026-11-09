"""Stage checkpoints.

The ingestion pipeline's resumable outputs and the record of each stage.

A document runs through ``Stage``s in order. Each stage's output is a ``StageArtifact``
stored under a ``CheckpointKey``: the org, the document, the content hash, the stage and
the stage's ``fingerprint`` (its code and model versions plus its upstream's fingerprint).
A stage whose fingerprint is unchanged reuses its artifact instead of running again, so a
redelivered message, a retry or a version bump of one stage re-runs only what changed.

``CheckpointStore`` holds the artifacts; ``StageStateClient`` records which stage finished
with which fingerprint (the record ``ResumePlanner`` reads). Artifacts are addressed by
``(org_id, content_hash, stage, fingerprint)``, so two documents of one org with the same
bytes share them; a document only holds a reference, and an artifact is deleted when no
reference is left. Artifacts are never shared across orgs.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, Protocol

if TYPE_CHECKING:
    from collections.abc import Collection, Mapping
    from datetime import datetime


class Stage(StrEnum):
    """An ingestion stage, declared in pipeline order."""

    PARSE = "parse"
    """Extract text and layout from the source file."""
    ENRICH = "enrich"
    """Add document-level metadata (language, entities, structure)."""
    CHUNK = "chunk"
    """Split the enriched text into retrieval chunks."""
    EMBED = "embed"
    """Embed each chunk (skipped when the sink embeds, as Bedrock does)."""
    INDEX = "index"
    """Write the chunks to the sink."""


@dataclass(frozen=True)
class CheckpointKey:
    """Where one stage's output for one document lives."""

    org_id: str
    """The owning org; artifacts are never shared across orgs."""
    document_id: str
    """The document; it holds a reference to the (possibly shared) artifact."""
    content_hash: str
    """Hash of the document's bytes; equal bytes share artifacts within an org."""
    stage: Stage
    """The stage that produced the artifact."""
    fingerprint: str
    """The stage's fingerprint (``foundation.checkpoint.fingerprint``)."""


@dataclass(frozen=True)
class StageArtifact:
    """One stage's output and the versions that produced it."""

    key: CheckpointKey
    """Where the artifact is stored."""
    payload: bytes
    """The stage's output, opaque to the store."""
    versions: Mapping[str, str] = field(default_factory=dict[str, str], hash=False)
    """The code and model versions the stage ran with (excluded from the hash)."""
    parts_done: frozenset[int] = frozenset()
    """The part numbers already written for this key (``CheckpointStore.put_part``)."""
    format_version: int = 1
    """The artifact envelope's format version."""
    attempt: int = 1
    """The delivery attempt that produced it; the stage record is conditional on it."""


@dataclass(frozen=True)
class StageState:
    """The record that a stage finished for a document with a given fingerprint."""

    org_id: str
    """The owning org."""
    document_id: str
    """The document."""
    stage: Stage
    """The finished stage."""
    content_hash: str
    """Hash of the bytes the stage ran on."""
    fingerprint: str
    """The fingerprint the stage ran with."""
    artifact_key: str
    """The storage key of the stage's artifact."""
    updated_at: datetime
    """When the record was written."""
    attempts: int
    """The delivery attempt that wrote it."""
    shard: int = 0
    """The store's partition component beside org and document (0 until the store
    shards)."""


class CheckpointStore(ABC):
    """Stores stage artifacts, shared within an org by content."""

    @abstractmethod
    async def get(self, key: CheckpointKey) -> StageArtifact | None:
        """Return the artifact at ``key``, or ``None`` when it is absent or unreadable."""
        ...

    @abstractmethod
    async def put(self, artifact: StageArtifact) -> None:
        """Store ``artifact``, record the document's reference to it.

        And its stage state.
        """
        ...

    @abstractmethod
    async def put_part(self, key: CheckpointKey, n: int, payload: bytes) -> None:
        """Store part ``n`` of a stage's output, for a stage that writes as it goes."""
        ...

    @abstractmethod
    async def get_part(self, key: CheckpointKey, n: int) -> bytes | None:
        """Return part ``n`` at ``key``, or ``None`` when it is absent."""
        ...

    @abstractmethod
    async def parts(self, key: CheckpointKey) -> frozenset[int]:
        """Return the part numbers stored at ``key``."""
        ...

    @abstractmethod
    async def delete(
        self, org_id: str, document_id: str, stages: Collection[Stage]
    ) -> None:
        """Drop the document's references for ``stages``.

        Unreferenced artifacts are deleted.
        """
        ...

    @abstractmethod
    async def purge(self, org_id: str, document_id: str) -> None:
        """Drop every reference the document holds.

        Deleting each artifact no one else uses.
        """
        ...


class StageStateClient(Protocol):
    """Reads and writes stage records.

    One service owns the table and is its only writer.
    """

    async def put_state(self, state: StageState) -> None:
        """Record ``state``.

        The write is conditional on ``(content_hash, fingerprint, attempts)``: a stale
        attempt raises ``ConflictError`` (``CONFLICT``), so a slow duplicate never
        overwrites a newer run.
        """
        ...

    async def get_states(self, org_id: str, document_id: str) -> list[StageState]:
        """Return every stage record of the document."""
        ...


class StageProgress(Protocol):
    """Reports that a stage is still working on a document, between its stage records.

    A long stage writes no record until it finishes; its heartbeat, stamped by the owning
    service's clock on the record of the upstream stage it reads, is what tells a reaper
    the document is alive rather than stuck.
    """

    async def heartbeat(self, org_id: str, document_id: str, upstream: Stage) -> None:
        """Report that the stage reading ``upstream``'s record.

        Of the document still works on it.

        A document with no ``upstream`` record records nothing; that is not an error.
        """
        ...
