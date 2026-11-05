"""``sink_from_config``: the config-selected ``Sink``."""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.sink.noop.sink import NoopSink
from techai_webutils.clients.sink.vector.sink import VectorStoreSink
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.sink import Sink, SinkStore


class SinkKind(StrEnum):
    """Which sink to build."""

    NOOP = "noop"
    """Stores nothing (the default)."""
    OPENSEARCH = "opensearch"
    """Writes chunks and vectors through the injected OpenSearch vector store."""


@dataclass(frozen=True, slots=True)
class SinkConfig:
    """Selects the sink and the collection it writes."""

    kind: SinkKind = SinkKind.NOOP
    """The sink to build; ``noop`` by default."""
    collection: str = "documents"
    """The collection (``opensearch``: the read alias) chunks are written to."""
    org_field: str = "org_id"
    """The metadata field carrying an entry's org; it must match the store's ``org_field``."""


def sink_from_config(config: SinkConfig, *, store: SinkStore | None = None) -> Sink:
    """Build the ``Sink`` ``config.kind`` selects.

    The ``opensearch`` kind writes through ``store``, the composition root's
    ``OpenSearchVectorStore``.

    Raises:
        AppValueError: An unknown kind, or ``opensearch`` without a store (``INVALID_INPUT``).

    """
    if config.kind == SinkKind.NOOP:
        return NoopSink()
    if config.kind == SinkKind.OPENSEARCH:
        if store is None:
            msg = "the opensearch sink needs a vector store"
            raise AppValueError(msg)
        return VectorStoreSink(store, config.collection, org_field=config.org_field)
    msg = f"unknown sink kind: {config.kind!r}"
    raise AppValueError(msg)
