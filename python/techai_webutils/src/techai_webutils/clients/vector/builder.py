"""Vector-store factory — Kind + Config + ``new_vector_store_from_config``.

Mirrors the env-aware logger factory: a ``StrEnum`` Kind selects the backend, a frozen ``Config``
carries the connection settings, and ``new_vector_store_from_config`` lazily imports the heavy backend
(``qdrant-client``) only for the kind actually requested.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.vector_store import VectorStore


class VectorStoreKind(StrEnum):
    """Selectable vector-store backends."""

    QDRANT = "qdrant"
    """The local Qdrant similarity-search store (dev)."""
    STUB = "stub"
    """In-process cosine-ranking stub (dev/test/all-stubs; no Qdrant)."""


@dataclass(frozen=True, slots=True)
class VectorStoreConfig:
    """Vector-store connection settings (the consumer maps its own config section onto it)."""

    kind: VectorStoreKind = VectorStoreKind.QDRANT
    """Selects the vector-store backend (only ``qdrant`` ships today)."""
    url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint."""
    collection: str = "documents"
    """Collection name created/queried by this store."""
    dimension: int = 768
    """Vector dimension for the collections this store creates (must match the embedder)."""


def new_vector_store_from_config(config: VectorStoreConfig) -> VectorStore:
    """Build the ``VectorStore`` selected by ``config.kind`` (heavy backend imported lazily).

    The ``AsyncQdrantClient`` is created here and lives for the returned store's lifetime — i.e. the
    process, since the store is wired once at startup (a server) or in a short-lived script. There is
    no separate teardown seam; the OS reclaims the connection on exit.
    """
    if config.kind is VectorStoreKind.QDRANT:
        from qdrant_client import AsyncQdrantClient  # noqa: PLC0415

        from techai_webutils.clients.vector.qdrant import QdrantVectorStore  # noqa: PLC0415

        # check_compatibility=False skips the best-effort server-version probe on construction: the
        # dev stack pins both client and server, and the probe otherwise makes a network call (and warns)
        # even when no server is up yet (e.g. at unit-test/wiring time).
        client = AsyncQdrantClient(url=config.url, check_compatibility=False)
        return QdrantVectorStore(client, dimension=config.dimension)
    if config.kind is VectorStoreKind.STUB:
        # Lazy import (no qdrant-client to load).
        from techai_webutils.clients.vector.stub import StubVectorStore  # noqa: PLC0415

        return StubVectorStore(dimension=config.dimension)
    msg = f"unknown vector store kind: {config.kind!r}"
    raise AppValueError(msg)
