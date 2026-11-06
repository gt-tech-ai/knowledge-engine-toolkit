"""Unit tests for the OpenSearch vector store's request shapes and mapping (the client is mocked).

The real OpenSearch behaviour (isolation, aliases, disk mode, domains) is proven in
``tests/integration/clients_vector_opensearch_integration_test.py``; these pin what is sent and how
responses and failures are read.
"""

from __future__ import annotations

from dataclasses import replace
from typing import cast
from unittest.mock import AsyncMock, MagicMock

import pytest

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
from techai_webutils.clients.vector.opensearch.mapping import (
    bulk_error,
    doc_id,
    entry_source,
    index_body,
    index_name,
    source_metadata,
)
from techai_webutils.core.errors import AppValueError, ErrorCode
from techai_webutils.core.interfaces.vector_store import VectorEntry
from techai_webutils.foundation.resilience.classify import is_throttled


def _client(*, alias_exists: bool = True) -> MagicMock:
    """Return a mocked ``AsyncOpenSearch`` whose alias exists and whose writes succeed."""
    client = MagicMock()
    client.indices.exists_alias = AsyncMock(return_value=alias_exists)
    client.indices.exists = AsyncMock(return_value=False)
    client.indices.create = AsyncMock()
    client.indices.update_aliases = AsyncMock()
    client.indices.get_alias = AsyncMock(return_value={})
    client.bulk = AsyncMock(return_value={"errors": False, "items": []})
    client.update_by_query = AsyncMock(return_value={})
    client.mget = AsyncMock()
    return client


def _entry(org: str | None = "o1", **metadata: str) -> VectorEntry:
    """Return one entry, in ``org`` unless ``None``."""
    meta = dict(metadata) | ({"org_id": org} if org else {})
    return VectorEntry(
        id="d:0", document_id="d", content="t", embedding=[1.0], metadata=meta
    )


def _vector_mapping(body: dict[str, object]) -> dict[str, object]:
    """Return the ``embedding`` field mapping of an index body."""
    mappings = cast("dict[str, dict[str, dict[str, object]]]", body["mappings"])
    return mappings["properties"]["embedding"]


@pytest.mark.asyncio
async def test_update_metadata_is_one_update_by_query_scoped_to_org_and_document() -> (
    None
):
    """Test that a metadata update sends one update-by-query filtered on the org and the document.

    **Why this test is important:**
      - A clearance change must touch every chunk of one document of one org, and nothing else.

    **What it tests:**
      - one ``update_by_query`` on the alias, routed by org, whose query filters ``org_id`` and
        ``document_id`` and whose script parameters are the fields
      - changing the org field itself is refused
    """
    client = _client()
    store = OpenSearchVectorStore(client, dimension=1).for_org("o1")

    await store.update_metadata("docs", "d1", {"clearance": "secret"})

    client.update_by_query.assert_awaited_once()
    kwargs = client.update_by_query.await_args.kwargs
    assert kwargs["index"] == "docs"
    assert kwargs["params"]["routing"] == "o1"
    assert kwargs["body"]["query"] == {
        "bool": {"filter": [{"term": {"org_id": "o1"}}, {"term": {"document_id": "d1"}}]}
    }
    assert kwargs["body"]["script"]["params"] == {"fields": {"clearance": "secret"}}
    with pytest.raises(AppValueError):
        await store.update_metadata("docs", "d1", {"org_id": "o2"})


@pytest.mark.asyncio
async def test_first_write_creates_the_versioned_index_and_alias() -> None:
    """Test that the first write creates ``<collection>-<model>-v<version>`` and points the alias at it.

    **Why this test is important:**
      - Reads always go through the alias, so a re-index can move it; the index must be stamped
        with its model so a mismatched write is caught.

    **What it tests:**
      - with no alias, an upsert creates ``docs-m1-v1`` stamped ``_meta {model: m1, version: 1}``,
        adds the ``docs`` alias, then bulk-writes the entry as ``o1/d:0`` routed by ``o1``
    """
    client = _client(alias_exists=False)
    store = OpenSearchVectorStore(
        client, dimension=1, config=OpenSearchConfig(model="m1")
    )

    await store.upsert("docs", [_entry()])

    create = client.indices.create.await_args.kwargs
    assert create["index"] == "docs-m1-v1"
    assert create["body"]["mappings"]["_meta"] == {"model": "m1", "version": "1"}
    actions = client.indices.update_aliases.await_args.kwargs["body"]["actions"]
    assert actions == [{"add": {"index": "docs-m1-v1", "alias": "docs"}}]
    header = client.bulk.await_args.kwargs["body"][0]["index"]
    assert header == {"_index": "docs", "_id": "o1/d:0", "routing": "o1"}


@pytest.mark.asyncio
async def test_dedicated_org_gets_its_own_index_beside_the_shared_one() -> None:
    """Test that an org listed in ``dedicated_orgs`` writes to its own alias, unrouted.

    **Why this test is important:**
      - Small orgs share one org-routed index so a domain is not spent on hundreds of tiny
        shards; a large org gets its own index so it neither crowds the shared shards nor
        is limited by them. Mixing the two must never send a shared org's chunks to the
        dedicated index, or the reverse.

    **What it tests:**
      - Under the shared layout with ``dedicated_orgs={"big"}``, ``big``'s write goes to the
        ``docs--big`` alias with no routing, while ``o1``'s goes to ``docs`` routed by ``o1``.
    """
    client = _client()
    store = OpenSearchVectorStore(
        client, dimension=1, config=OpenSearchConfig(dedicated_orgs=frozenset({"big"}))
    )

    await store.upsert("docs", [_entry("big")])
    big = client.bulk.await_args.kwargs["body"][0]["index"]
    await store.upsert("docs", [_entry("o1")])
    shared = client.bulk.await_args.kwargs["body"][0]["index"]

    assert big == {"_index": "docs--big", "_id": "big/d:0"}
    assert shared == {"_index": "docs", "_id": "o1/d:0", "routing": "o1"}


@pytest.mark.asyncio
async def test_scope_and_org_rules() -> None:
    """Test that every call without an org, or with another org than the scope, is refused.

    **Why this test is important:**
      - The store, not the caller, guarantees tenant isolation.

    **What it tests:**
      - unscoped delete, lookup, count and search without an org filter raise; an entry of org
        ``o2`` written through the ``o1`` view raises; a scoped entry without ``org_id`` is stored
        with it; the dedicated layout names the alias after the org
    """
    client = _client()
    store = OpenSearchVectorStore(client, dimension=1)
    for call in (
        store.delete("docs", ["x"]),
        store.stored_metadata("docs", ["x"]),
        store.count_by_document("docs", "x"),
        store.search("docs", [1.0]),
    ):
        with pytest.raises(AppValueError):
            await call
    with pytest.raises(AppValueError):
        await store.for_org("o1").upsert("docs", [_entry("o2")])
    with pytest.raises(AppValueError):
        store.for_org("")

    await store.for_org("o1").upsert("docs", [_entry(None)])
    assert client.bulk.await_args.kwargs["body"][1]["org_id"] == "o1"

    dedicated = OpenSearchVectorStore(
        client, dimension=1, config=OpenSearchConfig(layout=IndexLayout.DEDICATED)
    )
    await dedicated.for_org("O 1").upsert("Docs", [_entry("O 1")])
    header = client.bulk.await_args.kwargs["body"][0]["index"]
    assert header == {"_index": "docs--o-1", "_id": "O 1/d:0"}


@pytest.mark.asyncio
async def test_stored_metadata_reads_back_own_org_only() -> None:
    """Test that a lookup asks for org-qualified ids and keeps only the scoped org's documents.

    **Why this test is important:**
      - The resume probe must never report another org's chunk as already indexed.

    **What it tests:**
      - ``mget`` receives ``o1/a`` and ``o1/b``; a found document of another org is dropped;
        entity lists come back joined
    """
    client = _client()
    client.mget.return_value = {
        "docs": [
            {
                "_id": "o1/a",
                "found": True,
                "_source": {"entry_id": "a", "org_id": "o1", "entities.org": ["X", "Y"]},
            },
            {"_id": "o1/b", "found": True, "_source": {"entry_id": "b", "org_id": "o2"}},
            {"_id": "o1/c", "found": False},
        ]
    }
    store = OpenSearchVectorStore(client, dimension=1).for_org("o1")

    found = await store.stored_metadata("docs", ["a", "b"])

    assert client.mget.await_args.kwargs["body"] == {"ids": ["o1/a", "o1/b"]}
    assert found == {"a": {"org_id": "o1", "entities.org": "X|Y"}}


def test_bulk_error_classifies_failed_items() -> None:
    """Test that bulk responses map to no error, a transient error, or a permanent one.

    **Why this test is important:**
      - A throttled bulk must be retried; a mapping error must not be retried forever; a delete
        of a document already gone is success.
      - A rejected bulk (429, ``es_rejected_execution_exception``) is the domain pushing back;
        it must count as a throttle (``client_throttled_total``), or the bulk-rejection alert
        never fires.

    **What it tests:**
      - no ``errors`` → ``None``; a 429 item → ``UNAVAILABLE`` and a throttle; a 503 item →
        ``UNAVAILABLE`` but no throttle; a 400 item → ``INTERNAL``; a 404 on a delete → ``None``
    """
    throttled = {"errors": True, "items": [{"index": {"status": 429, "error": "busy"}}]}
    down = {"errors": True, "items": [{"index": {"status": 503, "error": "down"}}]}
    bad = {"errors": True, "items": [{"index": {"status": 400, "error": "mapping"}}]}
    gone = {"errors": True, "items": [{"delete": {"status": 404}}]}

    transient = bulk_error(throttled)
    unavailable = bulk_error(down)
    permanent = bulk_error(bad)

    assert bulk_error({"errors": False}) is None
    assert transient is not None
    assert transient.code is ErrorCode.UNAVAILABLE
    assert is_throttled(transient)
    assert unavailable is not None
    assert unavailable.code is ErrorCode.UNAVAILABLE
    assert not is_throttled(unavailable)
    assert permanent is not None
    assert permanent.code is ErrorCode.INTERNAL
    assert bulk_error(gone) is None


def test_mapping_helpers() -> None:
    """Test index naming, the index body's vector modes, and the entry ↔ source round trip.

    **Why this test is important:**
      - Names must be valid index names; the vector mapping decides memory cost; the round trip
        is what the resume probe compares.

    **What it tests:**
      - ``index_name("My Docs", "Titan v2", "3")`` is ``my-docs-titan-v2-v3``; ``doc_id`` prefixes
        the org; ``on_disk`` sets ``mode`` and compression, otherwise HNSW; a source maps back to
        exactly the entry's metadata
    """
    disk = _vector_mapping(index_body(4, "m", "1", on_disk=True, compression_level="16x"))
    memory = _vector_mapping(
        index_body(4, "m", "1", on_disk=False, compression_level="16x")
    )
    entry = _entry(**{"entities.person": "A|B", "tag": "x"})

    source = entry_source(entry, "org_id", "o1")

    assert index_name("My Docs", "Titan v2", "3") == "my-docs-titan-v2-v3"
    assert doc_id("o", "d:1") == "o/d:1"
    assert (disk["mode"], disk["compression_level"]) == ("on_disk", "16x")
    assert "method" in memory
    assert source["entities.person"] == ["A", "B"]
    assert source_metadata(source) == entry.metadata


def test_builder_and_multi_domain_config_errors() -> None:
    """Test that the factory refuses an opensearch store without clients or with bad shard routing.

    **Why this test is important:**
      - A store that routes a shard to a missing domain would lose writes; it must fail at boot.

    **What it tests:**
      - no injected client raises; a shard mapped to an unknown domain raises; an empty domain map
        raises; one client builds a single store, two build a multi-domain store
    """
    config = VectorStoreConfig(kind=VectorStoreKind.OPENSEARCH, dimension=1)
    bad = replace(config, opensearch=OpenSearchConfig(shard_domains=((0, "zz"),)))
    two = replace(config, opensearch=OpenSearchConfig(shard_domains=((0, "a"), (1, "b"))))

    with pytest.raises(AppValueError):
        new_vector_store_from_config(config)
    with pytest.raises(AppValueError):
        new_vector_store_from_config(bad, opensearch_clients={"a": _client()})
    with pytest.raises(AppValueError):
        MultiDomainVectorStore({}, {})
    single = new_vector_store_from_config(config, opensearch_clients={"a": _client()})
    multi = new_vector_store_from_config(
        two, opensearch_clients={"a": _client(), "b": _client()}
    )

    assert isinstance(single, OpenSearchVectorStore)
    assert isinstance(multi, MultiDomainVectorStore)
    assert multi.dimension == 1


@pytest.mark.asyncio
async def test_multi_domain_refuses_bad_shards() -> None:
    """Test that an entry's shard must be an integer mapped to a domain.

    **Why this test is important:**
      - A write to no domain must fail loudly, not vanish.

    **What it tests:**
      - shard ``"x"`` and unmapped shard ``"7"`` raise ``AppValueError``
    """
    multi = MultiDomainVectorStore(
        {"a": OpenSearchVectorStore(_client(), dimension=1)}, {0: "a"}
    )

    for shard in ("x", "7"):
        with pytest.raises(AppValueError):
            await multi.upsert("docs", [_entry(shard=shard)])


@pytest.mark.asyncio
async def test_multi_domain_spreads_unsharded_documents_by_id() -> None:
    """Test that entries naming no shard are spread over the domains by document id.

    **Why this test is important:**
      - A caller that does not assign shards (the sink writes none) must not pile the whole
        corpus onto the first domain, and one document's chunks must stay on one domain so a
        document never straddles domains.

    **What it tests:**
      - Forty documents without a shard land on both domains, each document's two chunks on
        the same one, and the same document id picks the same domain on a second write.
    """
    clients = {"a": _client(), "b": _client()}
    multi = MultiDomainVectorStore(
        {name: OpenSearchVectorStore(c, dimension=1) for name, c in clients.items()},
        {0: "a", 1: "b"},
    )
    entries = [
        VectorEntry(
            id=f"d{n}:{k}",
            document_id=f"d{n}",
            content="t",
            embedding=[1.0],
            metadata={"org_id": "o1"},
        )
        for n in range(40)
        for k in range(2)
    ]

    await multi.upsert("docs", entries)
    await multi.upsert("docs", entries[:2])

    placed: dict[str, set[str]] = {}
    for name, client in clients.items():
        for call in client.bulk.await_args_list:
            for op in call.kwargs["body"]:
                if "document_id" in op:
                    placed.setdefault(str(op["document_id"]), set()).add(name)
    assert all(len(domains) == 1 for domains in placed.values())
    assert {d for domains in placed.values() for d in domains} == {"a", "b"}
    assert len(placed) == 40
