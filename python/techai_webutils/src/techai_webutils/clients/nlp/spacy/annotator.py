"""``SpacyAnnotator``: entities from a spaCy pipeline.

Every component but ``ner`` disabled.

The pipeline is loaded on the first call (in a worker thread; construction does no I/O),
with ``enable=["ner"]`` so the tagger, parser and lemmatizer never run: entities are the
only task, and the disabled components are most of the CPU. Annotation runs in a worker
thread, one batch at a time (a spaCy pipeline is not safe to share across threads).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

import spacy

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

    from spacy.language import Language

    from techai_webutils.core.interfaces.annotator import TaskRegistry, TextBlock


class SpacyAnnotator(NoOpAsyncResource, Annotator):
    """Finds entities with a spaCy pipeline's ``ner`` component."""

    def __init__(
        self,
        pipeline: str,
        *,
        labels: tuple[str, ...],
        batch_size: int,
        registry: TaskRegistry,
    ) -> None:
        """Load ``pipeline`` (a package name or a path) on first use.

        Keep only ``labels`` (all if empty).
        """
        self._pipeline = pipeline
        self._labels = frozenset(labels)
        self._batch_size = batch_size
        self._registry = registry
        self._nlp: Language | None = None
        self._lock = asyncio.Lock()

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return each block's entities, in text order.

        (``profile`` does not change them).
        """
        require_entities_only(self._registry, tasks, "spacy")
        async with self._lock:
            if self._nlp is None:
                self._nlp = await asyncio.to_thread(
                    spacy.load, self._pipeline, enable=["ner"]
                )
            annotations = await asyncio.to_thread(self._annotate, self._nlp, blocks)
        return check_annotations(blocks, annotations, self._registry)

    def _annotate(self, nlp: Language, blocks: Sequence[TextBlock]) -> list[Annotation]:
        """Run the pipeline over the blocks' texts (in the worker thread)."""
        version = str(nlp.meta.get("version", ""))
        return [
            Annotation(
                entities=tuple(
                    Entity(ent.label_, ent.start_char, ent.end_char, ent.text)
                    for ent in doc.ents
                    if not self._labels or ent.label_ in self._labels
                ),
                model=self._pipeline,
                version=version,
            )
            for doc in nlp.pipe((b.text for b in blocks), batch_size=self._batch_size)
        ]
