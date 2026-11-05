"""Integration tests for the OpenSearch vector store against real OpenSearch (testcontainers).

Every test writes to its own collection, so the session's two clusters are shared safely. Writes
refresh at once (``refresh_on_write``) so they are searchable in the same test.
"""

from __future__ import annotations

import uuid
from dataclasses import replace
from typing import TYPE_CHECKING, cast

import pytest
import pytest_asyncio
from opensearchpy import AsyncOpenSearch

from techai_webutils.clients.vector import (
    VectorStoreConfig,
    VectorStoreKind,
    new_vector_store_from_config,
)
from techai_webutils.clients.vector.opensearch import (
    IndexLayout,
    MultiDomainVectorStore,
    OpenSearchConfig,
    OpenSearchVectorStore,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.vector_store import VectorEntry

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

pytestmark = pytest.mark.integration

_CONFIG = OpenSearchConfig(refresh_on_write=True, model="m1")


@pytest_asyncio.fixture
async def client(opensearch_url: str) -> AsyncIterator[AsyncOpenSearch]:
    """Yield a client on the first domain."""
    client = AsyncOpenSearch(hosts=[opensearch_url])
    try:
        yield client
    finally:
        await client.close()


@pytest_asyncio.fixture
async def client_b(opensearch_url_b: str) -> AsyncIterator[AsyncOpenSearch]:
    """Yield a client on the second domain."""
    client = AsyncOpenSearch(hosts=[opensearch_url_b])
    try:
        yield client
    finally:
        await client.close()


def _collection() -> str:
    """Return a collection name no other test uses."""
    return f"c{uuid.uuid4().hex[:10]}"


def _entry(doc: str, n: int, org: str, vector: list[float], **extra: str) -> VectorEntry:
    """Return chunk ``n`` of ``doc`` in ``org``."""
    return VectorEntry(
        id=f"{doc}:{n}",
        document_id=doc,
        content=f"text of {doc} chunk {n}",
        embedding=vector,
        metadata={"org_id": org, **extra},
    )


def _store(
    client: AsyncOpenSearch, config: OpenSearchConfig = _CONFIG
) -> OpenSearchVectorStore:
    """Build the single-domain store through the factory."""
    store = new_vector_store_from_config(
        VectorStoreConfig(
            kind=VectorStoreKind.OPENSEARCH, dimension=4, opensearch=config
        ),
        opensearch_clients={"a": client},
    )
    assert isinstance(store, OpenSearchVectorStore)
    return store


@pytest.mark.asyncio
async def test_round_trip_search_resume_and_delete(client: AsyncOpenSearch) -> None:
    """Test that entries round-trip with their metadata, resume via ``stored_metadata``, and delete.

    **Why this test is important:**
      - The indexer skips chunks already stored with the same metadata; a store that loses or
        rewrites metadata would re-embed everything on every redrive.

    **What it tests:**
      - a search in org ``o1`` returns the nearest chunk with its metadata and entity list
      - ``stored_metadata`` returns exactly the upserted metadata, and nothing for an unknown id
      - ``count_by_document`` counts 2, then 0 after ``delete_by_document``
    """
    collection = _collection()
    store = _store(client)
    entries = [
        _entry("d1", 0, "o1", [1, 0, 0, 0], **{"entities.person": "Ada|Grace"}),
        _entry("d1", 1, "o1", [0, 1, 0, 0], section_path="intro"),
    ]
    await store.upsert(collection, entries)
    scoped = store.for_org("o1")

    hits = await store.search(collection, [1, 0, 0, 0], 1, {"org_id": "o1"})
    stored = await scoped.stored_metadata(collection, ["d1:0", "d1:1", "nope"])

    assert [h.chunk_id for h in hits] == ["d1:0"]
    assert hits[0].metadata["entities.person"] == "Ada|Grace"
    assert stored == {"d1:0": entries[0].metadata, "d1:1": entries[1].metadata}
    assert await scoped.count_by_document(collection, "d1") == 2
    await scoped.delete_by_document(collection, "d1")
    assert await scoped.count_by_document(collection, "d1") == 0


@pytest.mark.asyncio
@pytest.mark.parametrize("layout", [IndexLayout.SHARED, IndexLayout.DEDICATED])
async def test_orgs_are_isolated(client: AsyncOpenSearch, layout: IndexLayout) -> None:
    """Test that two orgs with the same document ids never see each other's entries.

    **Why this test is important:**
      - Tenant isolation is a security boundary: one org's chunks must never reach another
        org's answers, in either index layout.

    **What it tests:**
      - org ``a`` and org ``b`` write the same ids; each org's search, lookup and count see only
        its own; deleting in ``a`` leaves ``b`` intact
    """
    collection = _collection()
    store = _store(client, replace(_CONFIG, layout=layout))
    await store.upsert(collection, [_entry("d", 0, "a", [1, 0, 0, 0], tag="A")])
    await store.upsert(collection, [_entry("d", 0, "b", [1, 0, 0, 0], tag="B")])
    a, b = store.for_org("a"), store.for_org("b")

    hits_a = await a.search(collection, [1, 0, 0, 0], 10)
    hits_b = await b.search(collection, [1, 0, 0, 0], 10)

    assert [h.metadata["tag"] for h in hits_a] == ["A"]
    assert [h.metadata["tag"] for h in hits_b] == ["B"]
    assert (await b.stored_metadata(collection, ["d:0"]))["d:0"]["tag"] == "B"
    await a.delete_by_document(collection, "d")
    assert await a.count_by_document(collection, "d") == 0
    assert await b.count_by_document(collection, "d") == 1


@pytest.mark.asyncio
async def test_calls_without_an_org_are_refused(client: AsyncOpenSearch) -> None:
    """Test that a search, count or write that names no org raises ``INVALID_INPUT``.

    **Why this test is important:**
      - A forgotten org filter would search every tenant; refusing it makes the mistake loud.

    **What it tests:**
      - a search without an org filter, an unscoped count, and an entry without ``org_id`` raise
        ``AppValueError``
    """
    collection = _collection()
    store = _store(client)
    no_org = VectorEntry(
        id="x", document_id="x", content="", embedding=[1, 0, 0, 0], metadata={}
    )

    with pytest.raises(AppValueError):
        await store.search(collection, [1, 0, 0, 0], 1)
    with pytest.raises(AppValueError):
        await store.count_by_document(collection, "x")
    with pytest.raises(AppValueError):
        await store.upsert(collection, [no_org])


async def _count(client: AsyncOpenSearch, index: str) -> int:
    """Return the documents in ``index`` (refreshed first)."""
    await client.indices.refresh(index=index)
    response = cast("dict[str, int]", await client.count(index=index))
    return response["count"]


@pytest.mark.asyncio
async def test_dual_writes_swap_and_swap_back(client: AsyncOpenSearch) -> None:
    """Test a re-index: writes land in both indices, the alias swaps, and swaps back.

    **Why this test is important:**
      - Moving to a new embedding model must not lose writes made during the backfill, and a bad
        new index must be reversible without re-ingesting.

    **What it tests:**
      - with two write targets, one upsert lands in both indices
      - after ``swap_alias`` reads come from the new index, stamped ``(m2, 1)``, and the old index
        still exists; swapping back restores the old index
    """
    collection = _collection()
    store = _store(client)
    await store.upsert(collection, [_entry("d", 0, "o", [1, 0, 0, 0])])
    old = await store.current_index(collection)
    new = await _store(client, replace(_CONFIG, model="m2")).create_index(collection)
    assert old is not None

    store.set_write_targets(collection, [old, new])
    await store.upsert(collection, [_entry("d", 1, "o", [0, 1, 0, 0])])
    await store.swap_alias(collection, new)

    assert await _count(client, old) == 2
    assert await _count(client, new) == 1
    assert await store.current_index(collection) == new
    assert await store.index_model(new) == ("m2", "1")
    assert await client.indices.exists(index=old)
    await store.swap_alias(collection, old)
    store.set_write_targets(collection, [])
    assert await store.current_index(collection) == old


@pytest.mark.asyncio
async def test_disk_mode_index_is_created_and_searchable(client: AsyncOpenSearch) -> None:
    """Test that an index expected above the threshold is ``on_disk`` and both kinds search.

    **Why this test is important:**
      - A large org's vectors do not fit in memory; ``on_disk`` keeps it servable at a fraction
        of the RAM, and must still answer searches.

    **What it tests:**
      - ``create_index(expected_docs=100)`` with a threshold of 10 maps the vector ``on_disk``;
        the default index uses HNSW in memory; a search through each returns the written chunk
    """
    collection = _collection()
    memory = _store(client, replace(_CONFIG, disk_mode_threshold_docs=10))
    await memory.upsert(collection, [_entry("d", 0, "o", [1, 0, 0, 0])])
    disk_store = _store(
        client, replace(_CONFIG, disk_mode_threshold_docs=10, model="disk")
    )
    disk = await disk_store.create_index(collection, expected_docs=100)
    disk_store.set_write_targets(collection, [disk])
    await disk_store.upsert(collection, [_entry("d", 0, "o", [1, 0, 0, 0])])
    await disk_store.swap_alias(collection, disk)

    mapping = cast(
        "dict[str, dict[str, dict[str, object]]]",
        await client.indices.get_mapping(index=disk),
    )
    vector = cast(
        "dict[str, dict[str, object]]", mapping[disk]["mappings"]["properties"]
    )["embedding"]

    assert vector["mode"] == "on_disk"
    hits = await disk_store.search(collection, [1, 0, 0, 0], 1, {"org_id": "o"})
    assert [h.chunk_id for h in hits] == ["d:0"]


async def _settings(client: AsyncOpenSearch, index: str) -> dict[str, object]:
    """Return ``index``'s index settings."""
    response = cast(
        "dict[str, dict[str, dict[str, dict[str, object]]]]",
        await client.indices.get_settings(index=index),
    )
    return response[index]["settings"]["index"]


async def _failing_backfill(client: AsyncOpenSearch, index: str) -> None:
    """Check bulk-ingest settings are in force, then fail like a backfill that died midway."""
    during = await _settings(client, index)
    assert (during["refresh_interval"], during["number_of_replicas"]) == ("-1", "0")
    msg = "backfill failed"
    raise RuntimeError(msg)


@pytest.mark.asyncio
async def test_bulk_ingest_mode_is_restored_even_after_a_failed_backfill(
    client: AsyncOpenSearch,
) -> None:
    """Test that bulk-ingest mode turns refresh and replicas off and ``end`` restores them.

    **Why this test is important:**
      - A backfill left with refresh off serves stale results forever; ``end`` must restore the
        settings even when the backfill raised, and be safe to call twice.

    **What it tests:**
      - after ``begin`` refresh is ``-1`` and replicas ``0``; a backfill raising inside
        ``try/finally`` still ends with refresh and replicas back to their recorded values;
        a second ``end`` is a no-op
    """
    collection = _collection()
    store = _store(client)
    await store.upsert(collection, [_entry("d", 0, "o", [1, 0, 0, 0])])
    index = await store.current_index(collection)
    assert index is not None
    await client.indices.put_settings(
        index=index, body={"index": {"refresh_interval": "2s"}}
    )
    before = await _settings(client, index)

    await store.begin_bulk_ingest(index)
    try:
        with pytest.raises(RuntimeError):
            await _failing_backfill(client, index)
    finally:
        await store.end_bulk_ingest(index)

    after = await _settings(client, index)
    assert after["refresh_interval"] == before["refresh_interval"] == "2s"
    assert after["number_of_replicas"] == before["number_of_replicas"]
    await store.end_bulk_ingest(index)


@pytest.mark.asyncio
async def test_two_domains_route_by_shard_and_search_fans_out(
    client: AsyncOpenSearch, client_b: AsyncOpenSearch
) -> None:
    """Test that shards land on their domains and a search returns an org's hits from both.

    **Why this test is important:**
      - Past one domain's size the corpus is split by shard; a search must still see every shard
        of the org, and never another org's.

    **What it tests:**
      - org ``o1``'s shard-0 chunk lands on domain ``a`` and its shard-1 chunk on ``b``
      - a search in ``o1`` returns both, none of ``o2``'s; a document count sums both domains
      - an entry on an unmapped shard raises ``AppValueError``
    """
    collection = _collection()
    config = replace(_CONFIG, shard_domains=((0, "a"), (1, "b")))
    store = new_vector_store_from_config(
        VectorStoreConfig(
            kind=VectorStoreKind.OPENSEARCH, dimension=4, opensearch=config
        ),
        opensearch_clients={"a": client, "b": client_b},
    )
    assert isinstance(store, MultiDomainVectorStore)
    await store.upsert(
        collection,
        [
            _entry("d", 0, "o1", [1, 0, 0, 0], shard="0"),
            _entry("d", 1, "o1", [0.9, 0.1, 0, 0], shard="1"),
            _entry("e", 0, "o2", [1, 0, 0, 0], shard="0"),
            _entry("e", 1, "o2", [1, 0, 0, 0], shard="1"),
        ],
    )

    hits = await store.search(collection, [1, 0, 0, 0], 10, {"org_id": "o1"})

    assert sorted(h.chunk_id for h in hits) == ["d:0", "d:1"]
    assert await store.domain("a").for_org("o1").count_by_document(collection, "d") == 1
    assert await store.domain("b").for_org("o1").count_by_document(collection, "d") == 1
    assert await store.for_org("o1").count_by_document(collection, "d") == 2
    with pytest.raises(AppValueError):
        await store.upsert(collection, [_entry("f", 0, "o1", [1, 0, 0, 0], shard="7")])
