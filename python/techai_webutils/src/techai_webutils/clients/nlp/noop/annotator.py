"""``NoopAnnotator``: annotates nothing (the factory's default; no model, no I/O)."""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.clients.nlp.entities import require_entities_only
from techai_webutils.core.interfaces.annotator import Annotation, Annotator
from techai_webutils.foundation.lifecycle import NoOpAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import TaskRegistry, TextBlock


class NoopAnnotator(NoOpAsyncResource, Annotator):
    """Returns an empty annotation per block."""

    def __init__(self, registry: TaskRegistry) -> None:
        """Check requested tasks against ``registry``."""
        self._registry = registry

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return one empty annotation per block (after refusing unknown tasks).

        Returns:
            One empty annotation per block (after refusing unknown tasks).

        """
        require_entities_only(self._registry, tasks, "noop")
        return [Annotation(model="noop") for _ in blocks]
