"""``PiiAnnotator``: adds the ``pii`` task to any annotator, in process.

When ``pii`` is asked for, each block's personal-data spans (``find_pii``) go in
``task_results["pii"]``; every other task goes to the wrapped annotator. When ``pii`` is
the only task, the wrapped annotator is not called. The registry must hold ``PII`` →
``PiiSpan`` (``registry.register(PII, PiiSpan)``), so an unregistered ``pii`` fails like
any unknown task.
"""

from __future__ import annotations

import dataclasses
from typing import TYPE_CHECKING, final, override

from techai_webutils.clients.nlp.pii.detect import PII, find_pii
from techai_webutils.core.interfaces.annotator import (
    Annotation,
    Annotator,
    check_annotations,
)
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import TaskRegistry, TextBlock


@final
class PiiAnnotator(DelegatingAsyncResource[Annotator], Annotator):
    """Answers the ``pii`` task itself and passes the rest to ``inner``."""

    def __init__(self, inner: Annotator, *, registry: TaskRegistry) -> None:
        """Wrap ``inner``; ``registry`` must register ``PII``."""
        super().__init__(inner)
        self._registry = registry

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return ``inner``'s annotations with each block's ``pii`` spans added.

        When asked.

        Returns:
            ``inner``'s annotations with each block's ``pii`` spans added.

        A callee raises ``AppValueError``: A task is not registered (``INVALID_INPUT``).

        """
        self._registry.require(tasks)
        rest = tasks - {PII}
        annotations = (
            await self._inner.annotate_batch(blocks, rest, profile)
            if rest
            else [Annotation() for _ in blocks]
        )
        if PII not in tasks:
            return annotations
        found = [
            dataclasses.replace(
                annotation,
                task_results={
                    **annotation.task_results,
                    PII: tuple(find_pii(block.text)),
                },
            )
            for block, annotation in zip(blocks, annotations, strict=True)
        ]
        return check_annotations(blocks, found, self._registry)
