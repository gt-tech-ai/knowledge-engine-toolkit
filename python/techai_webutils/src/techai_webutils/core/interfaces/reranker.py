"""Reranker interface: score retrieved passages against the query with a stronger model.

A retriever ranks by embedding similarity or BM25; a cross-encoder reads the query and
each passage together and scores their relevance directly, which reorders the candidate
pool far more accurately. ``Reranker`` is the seam the retrieval engine decorator calls;
``RerankClient`` is the consumer-side transport a ``cross_encoder`` reranker sends its
batches to (e.g. a gRPC stub to an NLP service), supplied by the composition root.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Sequence


class Reranker(ManagedResource, ABC):
    """Scores passages against a query; higher is more relevant."""

    @abstractmethod
    async def rerank(self, query: str, passages: Sequence[str]) -> list[float]:
        """Return one relevance score per passage, in the passages' order."""


class RerankClient(Protocol):
    """The transport a ``cross_encoder`` reranker scores one batch through."""

    async def score(self, query: str, passages: Sequence[str]) -> list[float]:
        """Return one cross-encoder score per passage, in order."""
        ...
