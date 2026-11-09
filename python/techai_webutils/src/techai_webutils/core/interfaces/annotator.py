"""``Annotator``: NLP annotations of text blocks (entities, plus tasks a consumer registers).

One call annotates a batch of blocks with the tasks asked for and a profile the backend may use
(e.g. a label set, or a consumer's ``interview`` profile). ``ENTITIES`` is the one built-in task;
a consumer registers more in a ``TaskRegistry`` with the result type its backend returns, with no
engine change. Sentence splitting and language identification are not annotator tasks: they run
in process (``SentenceSplitter``, ``LanguageIdentifier``).

Every backend's output goes through ``check_annotations``: one annotation per block, each entity
span inside its block, each task result of its registered type.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Protocol

from techai_webutils.core.errors import AppValueError, ConflictError, InternalError
from techai_webutils.core.interfaces.lifecycle import ManagedResource

if TYPE_CHECKING:
    from collections.abc import Iterable, Mapping, Sequence

ENTITIES = "entities"
"""The built-in task: named entities, returned in ``Annotation.entities``."""


@dataclass(frozen=True, slots=True)
class TextBlock:
    """A text to annotate."""

    text: str
    """The text."""
    block_id: str = ""
    """The caller's id for the block (e.g. a chunk id), echoed in logs and errors."""
    language: str = ""
    """The block's language code, when the caller identified it; empty when unknown."""


@dataclass(frozen=True, slots=True)
class TaskResult:
    """One result of a registered task; a task's result type subclasses it."""


@dataclass(frozen=True, slots=True)
class Entity(TaskResult):
    """A named entity: its label and its ``[start, end)`` character span in the block."""

    label: str
    """The entity type (``PERSON``, ``ORG``, or a configured label)."""
    start: int
    """Offset of the first character in the block."""
    end: int
    """Offset after the last character."""
    text: str
    """The block's text over the span."""
    confidence: float = 1.0
    """The backend's score, in [0, 1] (1.0 when it reports none)."""

    def __post_init__(self) -> None:
        """Reject an empty or inverted span.

        Raises:
            AppValueError: ``start`` is negative or not before ``end`` (``INVALID_INPUT``).

        """
        if self.start < 0 or self.end <= self.start:
            msg = f"entity {self.label!r} has an invalid span [{self.start}, {self.end})"
            raise AppValueError(msg)


@dataclass(frozen=True, slots=True)
class Annotation:
    """One block's annotations."""

    entities: tuple[Entity, ...] = ()
    """The ``ENTITIES`` task's results, in text order."""
    task_results: Mapping[str, tuple[TaskResult, ...]] = field(
        default_factory=dict[str, tuple[TaskResult, ...]]
    )
    """Each other requested task → its results."""
    model: str = ""
    """The model that annotated the block."""
    version: str = ""
    """The model's version."""
    degraded: bool = False
    """True when the backend was unavailable and the block was not annotated (empty results)."""


class TaskRegistry:
    """The tasks an annotator may be asked for, each with its result type.

    A consumer builds one at its composition root (``TaskRegistry()`` holds ``ENTITIES``) and
    registers its own tasks; there is no global registry.
    """

    def __init__(self) -> None:
        """Start with the built-in ``ENTITIES`` task."""
        self._tasks: dict[str, type[TaskResult]] = {ENTITIES: Entity}

    def register(self, name: str, result_type: type[TaskResult]) -> None:
        """Register task ``name`` returning ``result_type`` (registering it again is a no-op).

        Raises:
            ConflictError: ``name`` is registered with another result type.

        """
        current = self._tasks.get(name)
        if current is not None and current is not result_type:
            msg = f"task {name!r} is registered with {current.__name__}"
            raise ConflictError(msg)
        self._tasks[name] = result_type

    def get(self, name: str) -> type[TaskResult]:
        """Return task ``name``'s result type.

        Raises:
            AppValueError: The task is not registered (``INVALID_INPUT``).

        """
        result_type = self._tasks.get(name)
        if result_type is None:
            msg = f"unknown annotation task {name!r}; registered: {sorted(self._tasks)}"
            raise AppValueError(msg)
        return result_type

    def require(self, tasks: Iterable[str]) -> None:
        """Raise ``INVALID_INPUT`` unless every task is registered."""
        for name in tasks:
            self.get(name)


class Annotator(ManagedResource, ABC):
    """Annotates a batch of text blocks."""

    @abstractmethod
    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Return one annotation per block, in order, for the requested tasks.

        Raises:
            AppValueError: A task is not registered, or the backend cannot run it
                (``INVALID_INPUT``).

        """


class AnnotateClient(Protocol):
    """The transport a ``grpc`` annotator calls (the consumer adapts its generated stub to it)."""

    async def annotate_batch(
        self, blocks: Sequence[TextBlock], tasks: frozenset[str], profile: str
    ) -> list[Annotation]:
        """Annotate ``blocks`` remotely; a failure raises the transport's error."""
        ...


def check_annotations(
    blocks: Sequence[TextBlock],
    annotations: Sequence[Annotation],
    registry: TaskRegistry,
) -> list[Annotation]:
    """Return ``annotations`` after checking them against ``blocks`` and ``registry``.

    Raises:
        InternalError: Not one annotation per block, an entity span outside its block or not
            matching its text, or a task result of the wrong type (a backend fault).

    """
    if len(annotations) != len(blocks):
        msg = (
            f"annotator returned {len(annotations)} annotations for {len(blocks)} blocks"
        )
        raise InternalError(msg)
    for block, annotation in zip(blocks, annotations, strict=True):
        for entity in annotation.entities:
            if (
                entity.end > len(block.text)
                or block.text[entity.start : entity.end] != entity.text
            ):
                msg = f"entity span [{entity.start}, {entity.end}) does not match block {block.block_id!r}"
                raise InternalError(msg)
        for task, results in annotation.task_results.items():
            result_type = registry.get(task)
            if not all(isinstance(r, result_type) for r in results):
                msg = (
                    f"task {task!r} returned results that are not {result_type.__name__}"
                )
                raise InternalError(msg)
    return list(annotations)
