"""Unit tests for a multi-domain OpenSearch store behind the vector sink."""

from __future__ import annotations

from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

from techai_webutils.clients.sink.vector.sink import VectorStoreSink
from techai_webutils.clients.vector import VectorStoreConfig, VectorStoreKind
from techai_webutils.clients.vector.builder import opensearch_store_from_config
from techai_webutils.clients.vector.opensearch import (
    MultiDomainVectorStore,
    OpenSearchConfig,
)
from techai_webutils.core.errors import AppValueError, ConflictError
from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument


def _domain(index: str | None, model: tuple[str, str] = ("m", "1")) -> MagicMock:
    """Return a mocked domain client whose alias reads from ``index``.

    (none when ``None``).

    Args:
        index: The domain's index; ``None`` for none.
        model: The embedding model and version.

    Returns:
        A mocked domain client whose alias reads from ``index``.

    """
    client = MagicMock()
    cast("MagicMock", client.indices).exists_alias = AsyncMock(
        return_value=index is not None
    )
    cast("MagicMock", client.indices).get_alias = AsyncMock(
        return_value={index: {}} if index else {}
    )
    stamp = {"mappings": {"_meta": {"model": model[0], "version": model[1]}}}
    cast("MagicMock", client.indices).get_mapping = AsyncMock(
        side_effect=lambda **kw: {index: stamp} if kw["index"] == index else {}
    )
    client.update_by_query = AsyncMock(return_value={})
    return client


def _store(a: MagicMock, b: MagicMock) -> MultiDomainVectorStore:
    """Return a two-domain store.

    (shard 0 → ``a``, shard 1 → ``b``) built by the factory.

    Args:
        a: The first domain.
        b: The second domain.

    Returns:
        A two-domain store.

    """
    config = VectorStoreConfig(
        kind=VectorStoreKind.OPENSEARCH,
        dimension=2,
        opensearch=OpenSearchConfig(
            model="m", model_version="1", shard_domains=((0, "a"), (1, "b"))
        ),
    )
    store = opensearch_store_from_config(config, {"a": a, "b": b})
    assert isinstance(store, MultiDomainVectorStore)
    return store


def _document(model: str = "m") -> SinkDocument:
    """Return a document embedded with ``model`` version ``1``.

    Args:
        model: The embedding model and version.

    Returns:
        A document embedded with ``model`` version ``1``.

    """
    return SinkDocument(
        org_id="o1",
        document_id="d",
        content_hash="h",
        embedding_model=model,
        embedding_version="1",
    )


@pytest.mark.asyncio
async def test_multi_domain_store_reads_the_model_where_the_index_lives() -> None:
    """Test that a multi-domain store reports the live index and its model to the sink.

    **Why this test is important:**
      - A cell spread over several domains backs the ``opensearch`` sink; the sink refuses
        vectors from a model the index was not built for, so the store must find the index
        on whichever domain holds it, or a model swap would silently mix vector spaces.

    **What it tests:**
      - With only domain ``b`` holding the alias's index, ``current_index`` returns it,
        ``index_model`` reads its stamp from ``b`` (an index no domain holds is refused),
        and the sink refuses a document embedded with another model with
        ``ConflictError``.
    """
    store = _store(_domain(None), _domain("docs_o1_m_1"))
    org = store.for_org("o1")

    assert await org.current_index("docs") == "docs_o1_m_1"
    assert await org.index_model("docs_o1_m_1") == ("m", "1")
    assert store.configured_model == ("m", "1")
    with pytest.raises(AppValueError, match="no domain holds"):
        _ = await org.index_model("docs_o1_gone_1")
    with pytest.raises(ConflictError):
        await VectorStoreSink(store, "docs").write(
            _document(model="other"), [ChunkPayload(text="t")], [[0.1, 0.2]]
        )


@pytest.mark.asyncio
async def test_multi_domain_update_metadata_reaches_every_domain() -> None:
    """Test that a metadata update fans out to every domain.

    **Why this test is important:**
      - A document's chunks are spread over the shards' domains; an ACL change that
        reached one domain would leave the rest readable with the old clearance.

    **What it tests:**
      - ``update_metadata`` through the sink runs one ``update_by_query`` on each domain.
    """
    a, b = _domain("docs_o1_m_1"), _domain("docs_o1_m_1")

    await VectorStoreSink(_store(a, b), "docs").update_metadata(
        "o1", "d", {"clearance": "public"}
    )

    cast("MagicMock", cast("AsyncMock", a.update_by_query).assert_awaited_once)()
    cast("MagicMock", cast("AsyncMock", b.update_by_query).assert_awaited_once)()
