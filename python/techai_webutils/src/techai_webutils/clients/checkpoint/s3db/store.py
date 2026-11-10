"""``S3DbCheckpointStore``: artifacts in an object store.

Stage records go through a state client.

Artifacts are content-addressed within an org (``layout.Layout``), so documents with the
same bytes share them. Each document holds a reference marker, and each artifact a holder
marker per document; deleting a document's references deletes an artifact only once no
holder is left. A payload over ``max_artifact_bytes`` is written as segments beside the
envelope, so no single object outgrows the cap. A corrupt or unreadable envelope is logged
and read as a miss, so the stage simply runs again; an envelope from a newer release is
refused with a coded error. Compression runs on a worker thread, because compressing a
multi-megabyte artifact on the event loop would stall every other task in the process.
"""

from __future__ import annotations

import asyncio
import logging
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.clients.checkpoint.config import CheckpointCompression
from techai_webutils.clients.checkpoint.s3db.layout import (
    FORMAT_VERSION,
    Layout,
    compress,
    decode,
    decompress,
    encode,
    key_of,
)
from techai_webutils.core.errors import ConflictError, InvalidInputError
from techai_webutils.core.interfaces.checkpoint import (
    CheckpointKey,
    CheckpointStore,
    Stage,
    StageArtifact,
    StageState,
)

if TYPE_CHECKING:
    from collections.abc import Callable, Collection
    from datetime import datetime

    from techai_webutils.clients.checkpoint.config import CheckpointConfig
    from techai_webutils.core.interfaces.checkpoint import StageStateClient
    from techai_webutils.core.interfaces.storage import StorageClient

logger = logging.getLogger(__name__)

_CONTENT_TYPES = {
    CheckpointCompression.GZIP: "application/gzip",
    CheckpointCompression.ZSTD: "application/zstd",
}
_MARKER = b""


@final
class S3DbCheckpointStore(CheckpointStore):
    """Checkpoints in one bucket, with stage records written through ``state_client``."""

    def __init__(
        self,
        object_store: StorageClient,
        state_client: StageStateClient,
        config: CheckpointConfig,
        clock: Callable[[], datetime],
    ) -> None:
        """Store in ``config.bucket`` under ``config.prefix``.

        ``clock`` stamps the stage records.

        Args:
            object_store: Holds the envelopes, segments and parts.
            state_client: Holds the reference and holder markers.
            config: The bucket, prefix, compression and segment cap.
            clock: The time source stamped on artifacts.

        """
        self._objects = object_store
        self._states = state_client
        self._bucket = config.bucket
        self._layout = Layout(config.prefix, config.part_prefix)
        self._max = config.max_artifact_bytes
        self._compression = config.compression
        self._content_type = _CONTENT_TYPES[config.compression]
        self._clock = clock

    @override
    async def get(self, key: CheckpointKey) -> StageArtifact | None:
        """Return the artifact at ``key``; a missing or corrupt one is ``None``.

        Args:
            key: The artifact's checkpoint key.

        Returns:
            The artifact at ``key``; a missing or corrupt one is ``None``.

        Raises:
            ConflictError: The envelope was written by a newer release (``CONFLICT``).

        """
        name = self._layout.artifact(key)
        if not await self._objects.exists(self._bucket, name):
            return None
        try:
            body = await self._objects.download(self._bucket, name)
            header, payload = await asyncio.to_thread(decode, body)
            version = int(str(header["format_version"]))
            segments = int(str(header.get("segments", 0)))
        except (OSError, ValueError, KeyError, InvalidInputError):
            logger.warning(
                "checkpoint %s is corrupt; treating it as a miss", name, exc_info=True
            )
            return None
        if version > FORMAT_VERSION:
            msg = (
                f"checkpoint {name} has format {version}; this release reads up to "
                f"{FORMAT_VERSION}"
            )
            raise ConflictError(msg)
        if version < FORMAT_VERSION - 1:
            logger.warning(
                "checkpoint %s has unreadable format %s; a miss", name, version
            )
            return None
        if segments:
            payload = b"".join([await self._segment(key, n) for n in range(segments)])
        return StageArtifact(
            key,
            payload,
            versions={
                str(k): str(v) for k, v in _as_dict(header.get("versions")).items()
            },
            parts_done=frozenset(int(str(p)) for p in _as_list(header.get("parts_done"))),
            format_version=version,
        )

    @override
    async def put(self, artifact: StageArtifact) -> None:
        """Store ``artifact``, reference it and record its state.

        An equal artifact already stored is not written again.

        Segments are written before the envelope, so a reader never finds an envelope
        whose segments are missing.

        Args:
            artifact: The stage output to store, carrying its own key.

        """
        key = artifact.key
        name = self._layout.artifact(key)
        if not await self._objects.exists(self._bucket, name):
            await self._write(artifact, name)
        await self._mark(self._layout.holders(key) + key.document_id)
        await self._mark(self._layout.reference(key))
        await self._states.put_state(
            StageState(
                org_id=key.org_id,
                document_id=key.document_id,
                stage=key.stage,
                content_hash=key.content_hash,
                fingerprint=key.fingerprint,
                artifact_key=name,
                updated_at=self._clock(),
                attempts=artifact.attempt,
            )
        )

    @override
    async def put_part(self, key: CheckpointKey, n: int, payload: bytes) -> None:
        """Store part ``n`` at ``key``.

        Args:
            key: The stage output the part belongs to.
            n: The part number, from 0.
            payload: The part's bytes.

        """
        body = await asyncio.to_thread(compress, payload, self._compression)
        await self._objects.upload(
            self._bucket, self._layout.part(key, n), body, self._content_type
        )

    @override
    async def get_part(self, key: CheckpointKey, n: int) -> bytes | None:
        """Return part ``n`` at ``key``, or ``None`` when it is absent.

        Args:
            key: The stage output the part belongs to.
            n: The part number, from 0.

        Returns:
            Part ``n`` at ``key``, or ``None`` when it is absent.

        """
        name = self._layout.part(key, n)
        if not await self._objects.exists(self._bucket, name):
            return None
        body = await self._objects.download(self._bucket, name)
        return await asyncio.to_thread(decompress, body)

    @override
    async def parts(self, key: CheckpointKey) -> frozenset[int]:
        """Return the part numbers stored at ``key``.

        Args:
            key: The stage output whose parts are listed.

        Returns:
            The part numbers stored at ``key``.

        """
        prefix = self._layout.parts_prefix(key)
        found = await self._objects.list_objects(self._bucket, prefix)
        return frozenset(
            int(o.key[len(prefix) :].removesuffix(".json.gz")) for o in found
        )

    @override
    async def delete(
        self, org_id: str, document_id: str, stages: Collection[Stage]
    ) -> None:
        """Drop the document's references for ``stages``.

        Artifacts no one else holds are deleted.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document whose references are dropped.
            stages: The stages whose references are dropped.

        """
        refs = await self._objects.list_objects(
            self._bucket, self._layout.references(org_id, document_id)
        )
        for ref in refs:
            key = key_of(ref.key, org_id, document_id)
            if key.stage in stages:
                await self._release(key, ref.key)

    @override
    async def purge(self, org_id: str, document_id: str) -> None:
        """Drop every reference the document holds.

        Args:
            org_id: The tenant that owns the document.
            document_id: The document whose references are all dropped.

        """
        await self.delete(org_id, document_id, list(Stage))

    async def _write(self, artifact: StageArtifact, name: str) -> None:
        """Write the artifact's segments (when over the cap), then its envelope.

        Args:
            artifact: The artifact to store.
            name: The envelope's object key.

        """
        payload = artifact.payload
        segments = 0
        if len(payload) > self._max:
            chunks = [
                payload[i : i + self._max] for i in range(0, len(payload), self._max)
            ]
            for n, chunk in enumerate(chunks):
                body = await asyncio.to_thread(compress, chunk, self._compression)
                await self._objects.upload(
                    self._bucket,
                    self._layout.segment(artifact.key, n),
                    body,
                    self._content_type,
                )
            segments, payload = len(chunks), b""
        header = {
            "format_version": FORMAT_VERSION,
            "versions": dict(artifact.versions),
            "parts_done": sorted(artifact.parts_done),
            "segments": segments,
        }
        envelope = await asyncio.to_thread(encode, header, payload, self._compression)
        await self._objects.upload(self._bucket, name, envelope, self._content_type)

    async def _segment(self, key: CheckpointKey, n: int) -> bytes:
        """Return segment ``n`` of the artifact at ``key``.

        Args:
            key: The artifact's checkpoint key.
            n: The segment's index.

        Returns:
            Segment ``n`` of the artifact at ``key``.

        """
        body = await self._objects.download(self._bucket, self._layout.segment(key, n))
        return await asyncio.to_thread(decompress, body)

    async def _release(self, key: CheckpointKey, reference: str) -> None:
        """Drop one document's hold on an artifact.

        The artifact is deleted when no holder is left.

        Args:
            key: The artifact's checkpoint key.
            reference: The document's reference marker to drop.

        """
        await self._objects.delete(
            self._bucket, self._layout.holders(key) + key.document_id
        )
        await self._objects.delete(self._bucket, reference)
        if await self._objects.list_objects(self._bucket, self._layout.holders(key)):
            return
        for obj in await self._objects.list_objects(self._bucket, self._layout.base(key)):
            await self._objects.delete(self._bucket, obj.key)

    async def _mark(self, name: str) -> None:
        """Write an empty marker object.

        Args:
            name: The marker's object key.

        """
        await self._objects.upload(
            self._bucket, name, _MARKER, "application/octet-stream"
        )


def _as_list(value: object) -> list[object]:
    """Return ``value`` as a list (an absent or non-list header field is empty).

    Args:
        value: A decoded header field.

    Returns:
        ``value`` as a list (an absent or non-list header field is empty).

    """
    return list(cast("list[object]", value)) if isinstance(value, list) else []


def _as_dict(value: object) -> dict[object, object]:
    """Return ``value`` as a dict (an absent or non-dict header field is empty).

    Args:
        value: A decoded header field.

    Returns:
        ``value`` as a dict (an absent or non-dict header field is empty).

    """
    return dict(cast("dict[object, object]", value)) if isinstance(value, dict) else {}
