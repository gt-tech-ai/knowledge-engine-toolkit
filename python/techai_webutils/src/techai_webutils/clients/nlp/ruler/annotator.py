"""``EntityRulerAnnotator``: an org's own names, found exactly.

Before the statistical model.

A statistical NER model misses an org's product names, project code names and the
spellings its people use (an alias store holds them). The ruler matches each pattern's
phrase as whole words, case-insensitively, the longest phrase first, and its spans win: a
statistical entity that overlaps a ruler span is dropped, others are kept. The patterns
are the caller's (built per org, so the wrapper is cheap and holds no global state); with
none, the wrapped annotator's output is returned unchanged. Only the ``entities`` task is
touched; other tasks pass through.
"""

from __future__ import annotations

import dataclasses
import re
from dataclasses import dataclass
from typing import TYPE_CHECKING, override

from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    Annotation,
    Annotator,
    Entity,
)
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.annotator import TextBlock


@dataclass(frozen=True, slots=True)
class RulerPattern:
    """A phrase that is always an entity of ``label``."""

    label: str
    """The entity label to give the phrase."""
    phrase: str
    """The phrase, matched as whole words, case-insensitively."""

    def __post_init__(self) -> None:
        """Refuse an empty label or phrase.

        Raises:
            AppValueError: The label or the phrase is blank (``INVALID_INPUT``).

        """
        if not self.label.strip() or not self.phrase.strip():
            msg = (
                f"ruler pattern needs a label and a phrase: {self.label!r} "
                f"{self.phrase!r}"
            )
            raise AppValueError(msg)


class EntityRulerAnnotator(DelegatingAsyncResource[Annotator], Annotator):
    """Applies phrase patterns before the wrapped annotator.

    Pattern spans win on overlap.
    """

    def __init__(self, inner: Annotator, patterns: Sequence[RulerPattern]) -> None:
        """Wrap ``inner`` with ``patterns``.

        (the first pattern of a repeated phrase wins).
        """
        self._inner = inner
        self._labels: dict[str, str] = {}
        for pattern in patterns:
            self._labels.setdefault(pattern.phrase.casefold(), pattern.label)
        phrases = sorted(self._labels, key=len, reverse=True)
        self._regex = (
            re.compile(
                r"(?<!\w)(?:" + "|".join(re.escape(p) for p in phrases) + r")(?!\w)",
                re.IGNORECASE,
            )
            if phrases
            else None
        )

    @override
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Annotate through ``inner``, then put the ruler's entities first."""
        annotations = await self._inner.annotate_batch(blocks, tasks, profile)
        regex = self._regex
        if regex is None or ENTITIES not in tasks:
            return annotations
        return [
            _merge(annotation, _match(regex, self._labels, block.text))
            for block, annotation in zip(blocks, annotations, strict=True)
        ]


def _match(regex: re.Pattern[str], labels: dict[str, str], text: str) -> list[Entity]:
    """Return the patterns' matches in ``text``, labelled by their phrase."""
    return [
        Entity(labels[m.group(0).casefold()], m.start(), m.end(), m.group(0))
        for m in regex.finditer(text)
    ]


def _merge(annotation: Annotation, ruled: list[Entity]) -> Annotation:
    """Return ``annotation`` with ``ruled`` added.

    And the statistical spans they overlap dropped.
    """
    if not ruled:
        return annotation
    kept = [
        e
        for e in annotation.entities
        if not any(e.start < r.end and r.start < e.end for r in ruled)
    ]
    merged = tuple(sorted([*ruled, *kept], key=lambda e: (e.start, e.end)))
    return dataclasses.replace(annotation, entities=merged)
