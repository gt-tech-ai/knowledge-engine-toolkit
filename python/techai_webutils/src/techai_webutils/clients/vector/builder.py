"""Vector-store factory — Kind + Config + ``new_vector_store_from_config``.

Mirrors the env-aware logger factory: a ``StrEnum`` Kind selects the backend, a frozen ``Config``
carries the connection settings, and ``new_vector_store_from_config`` lazily imports the heavy backend
(``qdrant-client``) only for the kind actually requested.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING

from techai_webutils.clients.vector.opensearch import (
    MultiDomainVectorStore,
    OpenSearchConfig,
    OpenSearchVectorStore,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from opensearchpy import AsyncOpenSearch

    from techai_webutils.core.interfaces.vector_store import VectorStore


class VectorStoreKind(StrEnum):
    """Selectable vector-store backends."""

    QDRANT = "qdrant"
    """The local Qdrant similarity-search store (dev)."""
    STUB = "stub"
    """In-process cosine-ranking stub (dev/test/all-stubs; no Qdrant)."""
    OPENSEARCH = "opensearch"
    """OpenSearch k-NN + BM25, org-isolated, one or several domains (needs the ``opensearch`` extra)."""


@dataclass(frozen=True, slots=True)
class VectorStoreConfig:
    """Vector-store connection settings (the consumer maps its own config section onto it)."""

    kind: VectorStoreKind = VectorStoreKind.QDRANT
    """Selects the vector-store backend (``qdrant``, ``stub`` or ``opensearch``)."""
    url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint."""
    collection: str = "documents"
    """Collection name created/queried by this store."""
    dimension: int = 768
    """Vector dimension for the collections this store creates (must match the embedder)."""
    opensearch: OpenSearchConfig = field(default_factory=OpenSearchConfig)
    """Layout, model stamp, disk mode and shard routing of the ``opensearch`` kind."""


def new_vector_store_from_config(
    config: VectorStoreConfig,
    *,
    opensearch_clients: Mapping[str, AsyncOpenSearch] | None = None,
) -> VectorStore:
    """Build the ``VectorStore`` selected by ``config.kind`` (heavy backend imported lazily).

    The ``opensearch`` kind uses the injected ``opensearch_clients`` (domain name → client, built
    by the composition root with its hosts, SigV4 auth and timeouts): one domain builds an
    ``OpenSearchVectorStore``, several a ``MultiDomainVectorStore`` routed by
    ``config.opensearch.shard_domains``.

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
    if config.kind is VectorStoreKind.OPENSEARCH:
        return _opensearch_store(config, opensearch_clients)
    msg = f"unknown vector store kind: {config.kind!r}"
    raise AppValueError(msg)


def _opensearch_store(
    config: VectorStoreConfig, clients: Mapping[str, AsyncOpenSearch] | None
) -> VectorStore:
    """Build the single- or multi-domain OpenSearch store over the injected clients."""
    if not clients:
        msg = "the opensearch vector store needs at least one injected client"
        raise AppValueError(msg)
    stores = {
        name: OpenSearchVectorStore(
            client, dimension=config.dimension, config=config.opensearch
        )
        for name, client in clients.items()
    }
    if len(stores) == 1 and not config.opensearch.shard_domains:
        return next(iter(stores.values()))
    return MultiDomainVectorStore(
        stores,
        dict(config.opensearch.shard_domains),
        shard_field=config.opensearch.shard_field,
    )
