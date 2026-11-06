"""Integration tests for ``SnapshotAdmin`` against real OpenSearch (testcontainers).

The repository is an ``fs`` one under the container's ``path.repo``; the managed service's ``s3``
kind differs only in the repository body, which the unit tests pin.
"""

from __future__ import annotations

import uuid
from typing import TYPE_CHECKING

import pytest
import pytest_asyncio
from opensearchpy import AsyncOpenSearch

from techai_webutils.clients.vector.opensearch import (
    OpenSearchConfig,
    OpenSearchVectorStore,
)
from techai_webutils.clients.vector.opensearch.snapshots import (
    RepositoryKind,
    SnapshotAdmin,
    SnapshotRepository,
    SnapshotSchedule,
)
from techai_webutils.core.interfaces.vector_store import VectorEntry

if TYPE_CHECKING:
    from collections.abc import AsyncIterator

pytestmark = pytest.mark.integration


@pytest_asyncio.fixture
async def client(opensearch_url: str) -> AsyncIterator[AsyncOpenSearch]:
    """Yield a client on the domain."""
    client = AsyncOpenSearch(hosts=[opensearch_url])
    try:
        yield client
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_snapshot_restores_an_orgs_documents_under_a_new_name(
    client: AsyncOpenSearch, opensearch_snapshot_path: str
) -> None:
    """A snapshot of a shared index restores as a copy that answers the org's count and query.

    Why this test is important:
        The restore drill and a domain-loss recovery both depend on this path: the newest
        snapshot holding the index must restore beside (never over) the live index, and the copy
        must hold the org's documents and answer a query scoped to it.

    What it tests:
        - ``register`` then a snapshot of the store's index; ``latest`` finds it.
        - ``restore`` under a new name, ``wait_restored``, then ``count`` and ``sample`` for the
          org equal the source's, while another org's documents are not counted.
        - ``ensure_schedule`` twice leaves one policy at its first version (no rewrite at boot).
    """
    tag = uuid.uuid4().hex[:8]
    store = OpenSearchVectorStore(
        client, dimension=2, config=OpenSearchConfig(refresh_on_write=True)
    )
    entries = [
        VectorEntry(
            id=f"d:{n}",
            document_id="d",
            content=f"chunk {n}",
            embedding=[1.0, float(n)],
            metadata={"org_id": org},
        )
        for n, org in enumerate(["o1", "o1", "o1", "o2"])
    ]
    await store.upsert(f"c{tag}", entries)
    index = await store.for_org("o1").current_index(f"c{tag}")
    assert index is not None

    admin = SnapshotAdmin(client, timeout_seconds=30)
    repo = SnapshotRepository(
        name=f"r{tag}",
        kind=RepositoryKind.FS,
        location=f"{opensearch_snapshot_path}/{tag}",
    )
    await admin.register(repo)
    await client.snapshot.create(
        repository=repo.name,
        snapshot=f"s{tag}",
        body={"indices": index, "include_global_state": False},
        params={"wait_for_completion": "true"},
    )
    latest = await admin.latest(repo.name, index)
    assert latest is not None
    assert latest.name == f"s{tag}"

    copy = f"drill-{tag}"
    await admin.restore(repo.name, latest.name, index, as_index=copy)
    await admin.wait_restored(copy, 60)
    assert await admin.count(copy, field="org_id", value="o1") == 3
    assert await admin.sample(copy, field="org_id", value="o1") is not None
    assert await admin.sample(copy, field="org_id", value="o3") is None
    await admin.delete_index(copy)
    assert not await client.indices.exists(index=copy)

    schedule = SnapshotSchedule(policy=f"p{tag}", cron="0 * * * *", indices=index, keep=3)
    await admin.ensure_schedule(schedule, repo.name)
    first = await client.transport.perform_request(
        "GET", f"/_plugins/_sm/policies/p{tag}"
    )
    await admin.ensure_schedule(schedule, repo.name)
    second = await client.transport.perform_request(
        "GET", f"/_plugins/_sm/policies/p{tag}"
    )
    assert second["_seq_no"] == first["_seq_no"]
