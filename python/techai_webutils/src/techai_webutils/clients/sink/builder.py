"""``sink_from_config``: the config-selected ``Sink``."""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.sink.bedrock.config import BedrockSinkConfig
from techai_webutils.clients.sink.bedrock.sink import BedrockSink
from techai_webutils.clients.sink.noop.sink import NoopSink
from techai_webutils.clients.sink.vector.sink import VectorStoreSink
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.sink import Sink, SinkStore
    from techai_webutils.core.interfaces.storage import StorageClient


class SinkKind(StrEnum):
    """Which sink to build."""

    NOOP = "noop"
    """Stores nothing (the default)."""
    OPENSEARCH = "opensearch"
    """Writes chunks and vectors through the injected OpenSearch vector store."""
    BEDROCK = "bedrock"
    """Writes chunk files and sidecars for a Bedrock data source through the injected
    storage."""


@dataclass(frozen=True, slots=True)
class SinkConfig:
    """Selects the sink and where it writes."""

    kind: SinkKind = SinkKind.NOOP
    """The sink to build; ``noop`` by default."""
    collection: str = "documents"
    """The collection (``opensearch``: the read alias) chunks are written to."""
    org_field: str = "org_id"
    """The metadata field carrying an entry's org; it must match the store's ``org_field``
    (and the key the Bedrock retrieval filter reads)."""
    bedrock: BedrockSinkConfig = field(default_factory=BedrockSinkConfig)
    """The ``bedrock`` kind's bucket, prefix, layout and concurrency."""


def sink_from_config(
    config: SinkConfig,
    *,
    store: SinkStore | None = None,
    storage: StorageClient | None = None,
) -> Sink:
    """Build the ``Sink`` ``config.kind`` selects.

    The ``opensearch`` kind writes through ``store``, the composition root's
    ``OpenSearchVectorStore``; the ``bedrock`` kind through ``storage``, an object-store
    client for the data source's bucket.

    Raises:
        AppValueError: An unknown kind, ``opensearch`` without a store, or ``bedrock``
            without storage, a bucket or a positive concurrency (``INVALID_INPUT``).

    """
    if config.kind == SinkKind.NOOP:
        return NoopSink()
    if config.kind == SinkKind.OPENSEARCH:
        if store is None:
            msg = "the opensearch sink needs a vector store"
            raise AppValueError(msg)
        return VectorStoreSink(store, config.collection, org_field=config.org_field)
    if config.kind == SinkKind.BEDROCK:
        return _bedrock_sink(config, storage)
    msg = f"unknown sink kind: {config.kind!r}"
    raise AppValueError(msg)


def _bedrock_sink(config: SinkConfig, storage: StorageClient | None) -> Sink:
    """Build the ``bedrock`` sink, refusing a configuration that cannot write.

    Raises:
        AppValueError: No storage, no bucket, or a concurrency below one
            (``INVALID_INPUT``).

    """
    if storage is None:
        msg = "the bedrock sink needs a storage client"
        raise AppValueError(msg)
    if not config.bedrock.bucket:
        msg = "the bedrock sink needs the data source's bucket"
        raise AppValueError(msg)
    if config.bedrock.concurrency < 1:
        msg = (
            f"bedrock sink concurrency must be at least 1, got "
            f"{config.bedrock.concurrency}"
        )
        raise AppValueError(msg)
    return BedrockSink(storage, config.bedrock, org_field=config.org_field)
