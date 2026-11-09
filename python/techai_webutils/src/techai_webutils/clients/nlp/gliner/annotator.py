"""``GlinerAnnotator``.

Zero-shot entities for configured labels from an injected GLiNER model.

GLiNER finds entities of any label it is given (``Product``, ``Regulation``), which a
fixed spaCy label set cannot. The engine never imports ``gliner``: the composition root
loads the model (``GLiNER.from_pretrained``, with its torch stack) and injects it as a
``GlinerModel``. Each block runs in a worker thread, one batch at a time.
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, Protocol, cast, final, override

from techai_webutils.clients.nlp.entities import require_entities_only
from techai_webutils.core.interfaces.annotator import (
    Annotation,
    Annotator,
    Entity,
    check_annotations,
)
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import TaskRegistry, TextBlock


class GlinerModel(Protocol):
    """The slice of ``gliner.GLiNER`` the annotator calls."""

    def predict_entities(
        self, text: str, labels: list[str], threshold: float = 0.5
    ) -> list[dict[str, object]]:
        """Return the entities of ``labels`` in ``text``: dicts of start, end, text.

        Label, score.
        """
        ...


@final
class GlinerAnnotator(NoOpAsyncResource, Annotator):
    """Finds entities of the configured labels with an injected GLiNER model."""

    def __init__(
        self,
        model: GlinerModel,
        *,
        name: str,
        labels: tuple[str, ...],
        threshold: float,
        registry: TaskRegistry,
    ) -> None:
        """Annotate with ``model``.

        (reported as ``name``) for ``labels`` scoring at least ``threshold``.
        """
        self._model = model
        self._name = name
        self._labels = list(labels)
        self._threshold = threshold
        self._registry = registry
        self._lock = asyncio.Lock()

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return each block's entities of the configured labels, in text order.

        Returns:
            Each block's entities of the configured labels, in text order.

        """
        require_entities_only(self._registry, tasks, "gliner")
        async with self._lock:
            annotations = await asyncio.to_thread(self._annotate, blocks)
        return check_annotations(blocks, annotations, self._registry)

    def _annotate(self, blocks: Sequence[TextBlock]) -> list[Annotation]:
        """Run the model over each block (in the worker thread).

        Returns:
            One annotation per block.

        """
        return [
            Annotation(
                entities=tuple(
                    sorted(
                        (
                            _entity(found)
                            for found in self._model.predict_entities(
                                block.text, self._labels, threshold=self._threshold
                            )
                        ),
                        key=lambda e: e.start,
                    )
                ),
                model=self._name,
            )
            for block in blocks
        ]


def _entity(found: dict[str, object]) -> Entity:
    """Map one GLiNER prediction to an ``Entity``.

    Returns:
        The entity.

    """
    return Entity(
        label=str(found["label"]),
        start=cast("int", found["start"]),
        end=cast("int", found["end"]),
        text=str(found["text"]),
        confidence=float(cast("float", found.get("score", 1.0))),
    )
