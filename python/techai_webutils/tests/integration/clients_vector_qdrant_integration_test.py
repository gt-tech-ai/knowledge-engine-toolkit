"""Integration tests for the Qdrant vector store against a real Qdrant (testcontainers).

Every test writes to its own collection, so the session's node is shared safely.
"""

from __future__ import annotations

import uuid

import pytest

from techai_webutils.clients.vector import VectorStoreConfig, new_vector_store_from_config
from techai_webutils.core.interfaces.retrieval import FilterIn
from techai_webutils.core.interfaces.vector_store import LIST_SEPARATOR, VectorEntry

pytestmark = pytest.mark.integration

_DIMENSION = 4
"""Vector size; tiny keeps the test fast and the scores irrelevant to the assertions."""


@pytest.mark.asyncio
async def test_in_filter_matches_any_element_of_a_list_payload(qdrant_url: str) -> None:
    """An ``in`` filter finds a chunk whose list metadata holds any requested value.

    **Why this test is important:**
      - Tags are indexed as a list payload and narrowed with ``where``. Only a real Qdrant
        proves a tuple is stored as an array that ``MatchAny`` matches element-wise, and
        that the array reads back joined rather than as a Python repr.

    **What it tests:**
      - ``where=FilterIn("tags", ("api",))`` returns exactly the chunk tagged
        ``("api", "auth")``, not the one tagged ``("ops",)``.
      - Combined with ``filters`` the two narrow together (AND).
      - The hit's ``tags`` metadata reads back as ``"api|auth"``.
    """
    collection = f"c-{uuid.uuid4().hex}"
    store = new_vector_store_from_config(
        VectorStoreConfig(url=qdrant_url, dimension=_DIMENSION, collection=collection)
    )
    vector = [1.0, 0.0, 0.0, 0.0]
    await store.upsert(
        collection,
        [
            VectorEntry(
                id="a",
                document_id="d1",
                content="api auth",
                embedding=vector,
                metadata={"tags": ("api", "auth"), "space": "eng"},
            ),
            VectorEntry(
                id="b",
                document_id="d2",
                content="ops",
                embedding=vector,
                metadata={"tags": ("ops",), "space": "eng"},
            ),
        ],
    )

    hits = await store.search(
        collection, vector, top_k=10, where=FilterIn("tags", ("api", "missing"))
    )
    narrowed = await store.search(
        collection,
        vector,
        top_k=10,
        filters={"space": "other"},
        where=FilterIn("tags", ("api",)),
    )

    assert [hit.document_id for hit in hits] == ["d1"]
    assert hits[0].metadata["tags"] == LIST_SEPARATOR.join(("api", "auth"))
    assert narrowed == []
