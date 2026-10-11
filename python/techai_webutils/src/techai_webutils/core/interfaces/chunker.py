"""Chunker stage-collaborator contract: split parsed text into retrieval-sized chunks.

The embedding-granularity chunk seam a ``KnowledgeBase`` indexer applies per index unit (NOT a
standalone stage — an index-stage collaborator). Config-selected backends in
``techai_webutils.clients.chunking`` satisfy it; structure-aware/semantic backends are future
``Kind``s. Imported by full submodule path (not re-exported from
``core.interfaces``).
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable


@runtime_checkable
class Chunker(Protocol):
    """Split document text into retrieval-sized chunks for embedding (one backend per Kind)."""

    def chunk(self, text: str) -> list[str]:
        """Return the non-empty, size-bounded chunks of ``text`` (deterministic — reproducible indexing)."""
        ...
