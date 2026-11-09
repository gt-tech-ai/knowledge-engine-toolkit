"""Vector-store factory — Kind + Config + ``new_vector_store_from_config``.

Mirrors the env-aware logger factory: a ``StrEnum`` Kind selects the backend, a frozen
``Config`` carries the connection settings, and ``new_vector_store_from_config`` lazily
imports the heavy backend (``qdrant-client``) only for the kind actually requested.
"""

from __future__ import annotations

import importlib
from dataclasses import dataclass, field
from enum import StrEnum
from typing import TYPE_CHECKING, cast

from techai_webutils.clients.vector.opensearch import (
    MultiDomainVectorStore,
    OpenSearchConfig,
    OpenSearchVectorStore,
)
from techai_webutils.core.errors import AppValueError

if TYPE_CHECKING:
    from collections.abc import Mapping

    from opensearchpy import AsyncOpenSearch
    from qdrant_client import AsyncQdrantClient as _LazyAsyncQdrantClient

    from techai_webutils.clients.vector.qdrant import (
        QdrantVectorStore as _LazyQdrantVectorStore,
    )
    from techai_webutils.clients.vector.stub import (
        StubVectorStore as _LazyStubVectorStore,
    )
    from techai_webutils.core.interfaces.metrics import MetricsProvider
    from techai_webutils.core.interfaces.vector_store import VectorStore


class VectorStoreKind(StrEnum):
    """Selectable vector-store backends."""

    QDRANT = "qdrant"
    """The local Qdrant similarity-search store (dev)."""
    STUB = "stub"
    """In-process cosine-ranking stub (dev/test/all-stubs; no Qdrant)."""
    OPENSEARCH = "opensearch"
    """OpenSearch k-NN + BM25, org-isolated, one or several domains (needs the
    ``opensearch`` extra)."""


@dataclass(frozen=True, slots=True)
class VectorStoreConfig:
    """Vector-store connection settings.

    (the consumer maps its own config section onto it).
    """

    kind: VectorStoreKind = VectorStoreKind.QDRANT
    """Selects the vector-store backend (``qdrant``, ``stub`` or ``opensearch``)."""
    url: str = "http://localhost:6333"
    """Qdrant HTTP endpoint."""
    collection: str = "documents"
    """Collection name created/queried by this store."""
    dimension: int = 768
    """Vector dimension for the collections this store creates (must match the
    embedder)."""
    opensearch: OpenSearchConfig = field(default_factory=OpenSearchConfig)
    """Layout, model stamp, disk mode and shard routing of the ``opensearch`` kind."""


def _qdrant_store(config: VectorStoreConfig) -> VectorStore:
    """Build the Qdrant store, loading ``qdrant-client`` only now.

    Returns:
        The store over a client for ``config.url``.

    """
    async_qdrant_client_cls = cast(
        "type[_LazyAsyncQdrantClient]",
        importlib.import_module("qdrant_client").AsyncQdrantClient,
    )

    qdrant_vector_store_cls = cast(
        "type[_LazyQdrantVectorStore]",
        importlib.import_module(
            "techai_webutils.clients.vector.qdrant"
        ).QdrantVectorStore,
    )

    # check_compatibility=False skips the best-effort server-version probe on
    # construction: the dev stack pins both client and server, and the probe otherwise
    # makes a network call (and warns) even when no server is up yet (e.g. at
    # unit-test/wiring time).
    client = async_qdrant_client_cls(url=config.url, check_compatibility=False)
    return qdrant_vector_store_cls(client, dimension=config.dimension)


def _stub_store(config: VectorStoreConfig) -> VectorStore:
    """Build the in-process stub store, which needs no ``qdrant-client``.

    Returns:
        The stub store of ``config.dimension``.

    """
    # Lazy import (no qdrant-client to load).
    stub_vector_store_cls = cast(
        "type[_LazyStubVectorStore]",
        importlib.import_module("techai_webutils.clients.vector.stub").StubVectorStore,
    )

    return stub_vector_store_cls(dimension=config.dimension)


def new_vector_store_from_config(
    config: VectorStoreConfig,
    *,
    opensearch_clients: Mapping[str, AsyncOpenSearch] | None = None,
) -> VectorStore:
    """Build the ``VectorStore`` selected by ``config.kind``.

    (heavy backend imported lazily).

    The ``opensearch`` kind uses the injected ``opensearch_clients`` (domain name →
    client, built by the composition root with its hosts, SigV4 auth and timeouts): one
    domain builds an ``OpenSearchVectorStore``, several a ``MultiDomainVectorStore``
    routed by ``config.opensearch.shard_domains``.

    The ``AsyncQdrantClient`` is created here and lives for the returned store's lifetime
    — i.e. the process, since the store is wired once at startup (a server) or in a
    short-lived script. There is no separate teardown seam; the OS reclaims the connection
    on exit.

    Returns:
        The ``VectorStore`` selected by ``config.kind``.

    Raises:
        AppValueError: Unknown vector store kind: ``config.kind``.

    """
    if config.kind is VectorStoreKind.QDRANT:
        return _qdrant_store(config)
    if config.kind is VectorStoreKind.STUB:
        return _stub_store(config)
    if config.kind is VectorStoreKind.OPENSEARCH:
        return opensearch_store_from_config(config, opensearch_clients)
    msg = f"unknown vector store kind: {config.kind!r}"
    raise AppValueError(msg)


def opensearch_store_from_config(
    config: VectorStoreConfig,
    clients: Mapping[str, AsyncOpenSearch] | None,
    *,
    metrics: MetricsProvider | None = None,
) -> OpenSearchVectorStore | MultiDomainVectorStore:
    """Build the single- or multi-domain OpenSearch store over the injected clients.

    ``new_vector_store_from_config`` returns it as a ``VectorStore``; a composition root
    that hands the store to the ``opensearch`` sink calls this directly, because both
    concrete stores are also ``SinkStore``s (index model, alias and metadata-update
    calls). ``metrics`` counts the domains a multi-domain search cuts at
    ``domain_deadline_seconds``.

    Returns:
        The single- or multi-domain OpenSearch store over the injected clients.

    Raises:
        AppValueError: No injected client, or a shard mapped to an unknown domain
            (``INVALID_INPUT``).

    """
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
        deadline_seconds=config.opensearch.domain_deadline_seconds,
        metrics=metrics,
    )
