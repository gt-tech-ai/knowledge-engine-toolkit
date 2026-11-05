"""``DegradingAnnotator``: keep enriching, visibly, while the NLP backend is down.

Entities are an enrichment, not the document: when the primary raises a transient error (its
breaker is open, the service is unreachable or slow), the decorator returns one empty
``Annotation(degraded=True)`` per block, with no fallback model, and counts
``nlp_degraded_total{task}`` per block and task. A permanent error is re-raised, never masked. The
factory applies it with ``AnnotatorConfig.on_unavailable = degrade``; ``wait`` (the default)
re-raises the transient error so the caller retries.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppError
from techai_webutils.core.interfaces.annotator import Annotation, Annotator
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import TextBlock
    from techai_webutils.core.interfaces.metrics import MetricCounter, MetricsProvider

DEGRADED_METRIC = "nlp_degraded_total"
"""Blocks returned unannotated because the backend was unavailable, by task."""


class DegradingAnnotator(DelegatingAsyncResource[Annotator], Annotator):
    """Returns empty, degraded annotations when the primary fails transiently."""

    def __init__(
        self, primary: Annotator, *, metrics: MetricsProvider | None = None
    ) -> None:
        """Wrap ``primary``; ``metrics`` counts degraded blocks (``None`` counts nothing)."""
        self._inner = primary
        self._degraded: MetricCounter | None = (
            metrics.counter(
                DEGRADED_METRIC,
                "Blocks left unannotated because the NLP backend was unavailable.",
                ["task"],
            )
            if metrics is not None
            else None
        )

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Annotate through the primary; on a transient error, return degraded annotations.

        Raises:
            AppError: A permanent error from the primary, unchanged.

        """
        try:
            return await self._inner.annotate_batch(blocks, tasks, profile)
        except AppError as exc:
            if not exc.is_transient:
                raise
        if self._degraded is not None:
            for task in tasks:
                self._degraded.inc(float(len(blocks)), task=task)
        return [Annotation(degraded=True) for _ in blocks]
