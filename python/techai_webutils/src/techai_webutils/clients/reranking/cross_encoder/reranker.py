"""``CrossEncoderReranker``.

Score passages through an injected cross-encoder ``RerankClient``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import InternalError
from techai_webutils.core.interfaces.reranker import Reranker
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.reranker import RerankClient


class CrossEncoderReranker(NoOpAsyncResource, Reranker):
    """Scores passages in batches of ``batch_size`` through the client.

    One batch at a time.

    The client (e.g. a gRPC stub to an NLP service) belongs to the composition root, which
    owns its lifetime; batches bound one request's size on the model server.
    """

    def __init__(self, client: RerankClient, *, batch_size: int) -> None:
        """Bind the client and the most passages sent per call."""
        self._client = client
        self._batch_size = batch_size

    @override
    async def rerank(self, query: str, passages: Sequence[str]) -> list[float]:
        """Return the client's score for each passage, in order.

        Raises:
            InternalError: The client returned a different number of scores than passages.

        """
        scores: list[float] = []
        for start in range(0, len(passages), self._batch_size):
            batch = passages[start : start + self._batch_size]
            batch_scores = await self._client.score(query, batch)
            if len(batch_scores) != len(batch):
                msg = (
                    f"rerank client returned {len(batch_scores)} scores for "
                    f"{len(batch)} passages"
                )
                raise InternalError(msg)
            scores.extend(float(s) for s in batch_scores)
        return scores
