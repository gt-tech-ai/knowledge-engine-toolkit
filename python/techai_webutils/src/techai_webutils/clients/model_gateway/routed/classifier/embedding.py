"""The ``embedding`` route classifier: nearest route by exemplar similarity.

Each route with exemplars is scored by the mean of the ``top_k`` cosine similarities between the
query and its exemplars; the best route wins when its score reaches the route's ``threshold``,
otherwise the step's own route (the stub's rule) serves with that score as confidence. The
exemplars are embedded once, at ``warm()`` (the gateway's start-up) — a failure there fails the
boot with ``UNAVAILABLE``. At request time an embedder failure degrades to the step's route
(``degraded=True``): routing is an optimisation, never a dependency of the answer.
"""

from __future__ import annotations

import math
from typing import TYPE_CHECKING

from techai_webutils.core.errors import AppError, ErrorCode
from techai_webutils.core.types.routing import Classification

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.clients.model_gateway.routed.classifier.stub import (
        StubRouteClassifier,
    )
    from techai_webutils.core.interfaces.embedding import EmbeddingProvider
    from techai_webutils.core.interfaces.model_gateway import RouteContext
    from techai_webutils.core.types.routing import Route


def _cosine(a: Sequence[float], b: Sequence[float]) -> float:
    """Return the cosine similarity of ``a`` and ``b`` (0.0 for a zero vector)."""
    dot = sum(x * y for x, y in zip(a, b, strict=False))
    norm = math.sqrt(sum(x * x for x in a)) * math.sqrt(sum(y * y for y in b))
    return dot / norm if norm else 0.0


class EmbeddingRouteClassifier:
    """Classifies a query into the route whose exemplars it is most similar to."""

    def __init__(
        self,
        routes: Mapping[str, Route],
        embedding: EmbeddingProvider,
        fallback: StubRouteClassifier,
        top_k: int = 3,
    ) -> None:
        """Bind the routes, the (memoized) embedder, the step-route fallback and ``top_k``."""
        self._routes = {name: r for name, r in routes.items() if r.exemplars}
        self._embedding = embedding
        self._fallback = fallback
        self._top_k = top_k
        self._exemplars: dict[str, list[list[float]]] | None = None

    async def warm(self) -> None:
        """Embed every exemplar once (one batch), as a query: an exemplar is an example query.

        Raises:
            AppError: ``UNAVAILABLE`` when the exemplars can't be embedded (fails the boot).

        """
        names = [(name, text) for name, r in self._routes.items() for text in r.exemplars]
        try:
            vectors = await self._embedding.embed_batch(
                [text for _, text in names], input_type="query"
            )
        except Exception as exc:
            msg = "route exemplars could not be embedded"
            raise AppError(ErrorCode.UNAVAILABLE, msg, cause=exc) from exc
        exemplars: dict[str, list[list[float]]] = {}
        for (name, _), result in zip(names, vectors, strict=True):
            exemplars.setdefault(name, []).append(result.embedding)
        self._exemplars = exemplars

    def _scores(self, query: Sequence[float]) -> dict[str, float]:
        """Return each route's mean top-k exemplar similarity to ``query``."""
        scores: dict[str, float] = {}
        for name, vectors in (self._exemplars or {}).items():
            best = sorted((_cosine(query, v) for v in vectors), reverse=True)[
                : self._top_k
            ]
            scores[name] = sum(best) / len(best)
        return scores

    async def classify(self, ctx: RouteContext, query: str) -> Classification:
        """Return the most similar route above its threshold, else the step's route."""
        fallback = await self._fallback.classify(ctx, query)
        try:
            if self._exemplars is None:
                await self.warm()
            vector = (await self._embedding.embed(query, input_type="query")).embedding
        except Exception:
            return Classification(route=fallback.route, confidence=0.0, degraded=True)
        scores = self._scores(vector)
        if not scores:
            return fallback
        best = max(scores, key=lambda name: scores[name])
        if scores[best] >= self._routes[best].threshold:
            return Classification(route=best, confidence=scores[best])
        return Classification(route=fallback.route, confidence=scores[best])
