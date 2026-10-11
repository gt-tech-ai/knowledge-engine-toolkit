"""The hot per-item dataclasses are slotted (no per-instance ``__dict__``).

One of these is allocated per chunk on every upsert and per hit on every query, so across a bulk
index millions are created; a per-instance ``__dict__`` is ~100+ bytes of avoidable overhead each.
``slots=True`` removes it.
"""

from __future__ import annotations

from techai_webutils.core.interfaces.embedding import EmbeddingResult
from techai_webutils.core.interfaces.retrieval import Citation, RetrievalResult
from techai_webutils.core.interfaces.vector_store import VectorEntry, VectorSearchResult


def _hot_instances() -> list[object]:
    """One instance of each hot dataclass, constructed with valid fields."""
    return [
        VectorSearchResult(
            document_id="d", chunk_id="c", content="x", score=1.0, metadata={}
        ),
        VectorEntry(id="i", document_id="d", content="x", embedding=[0.1], metadata={}),
        EmbeddingResult(embedding=[0.1], model="m", token_count=1),
        RetrievalResult(
            document_id="d",
            document_name="n",
            chunk_content="x",
            score=1.0,
            page_number=1,
            metadata={},
        ),
        Citation(
            document_id="d", document_name="n", chunk="x", page_number=1, confidence=0.9
        ),
    ]


class TestHotDataclassSlots:
    """Tests for the hot-path dataclasses' slots."""

    def test_hot_dataclasses_are_slotted(self) -> None:
        """Test that the five hot dataclasses carry no per-instance ``__dict__``.

        **Why this test is important:**
          - These are the highest-frequency instances in the indexing + query paths; a per-instance
            ``__dict__`` is avoidable heap/GC overhead multiplied by millions across a bulk index.

        **What it tests:**
          - No instance of VectorSearchResult / VectorEntry / EmbeddingResult / RetrievalResult /
            Citation has a ``__dict__`` (i.e. ``slots=True`` is in effect).
        """
        for inst in _hot_instances():
            assert not hasattr(inst, "__dict__"), (
                f"{type(inst).__name__} should be slotted (no __dict__)"
            )
