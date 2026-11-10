"""Unit tests for the annotator types, the task registry and the annotation check."""

from __future__ import annotations

import dataclasses
from dataclasses import dataclass

import pytest

from techai_webutils.core.errors import AppError, AppValueError, ConflictError, ErrorCode
from techai_webutils.core.interfaces.annotator import (
    ENTITIES,
    Annotation,
    Entity,
    TaskRegistry,
    TaskResult,
    TextBlock,
    check_annotations,
)


@dataclass(frozen=True, slots=True)
class Speaker(TaskResult):
    """A consumer task's result: who speaks a turn."""

    name: str
    """The speaker."""


def test_types_are_frozen_and_spans_are_validated() -> None:
    """Test that the annotator's types are immutable.

    An entity's span must be well formed.

    **Why this test is important:**
      - Annotations travel from the NLP service into chunk metadata; a mutable result or
        an inverted span would corrupt the chunk's entities silently.

    **What it tests:**
      - assigning to an ``Entity``, an ``Annotation`` or a ``TextBlock`` raises
      - a negative start or an end not after the start raises ``AppValueError``
    """
    targets: list[tuple[object, str]] = [
        (Entity("ORG", 0, 4, "Acme", 0.9), "label"),
        (Annotation(), "degraded"),
        (TextBlock("t"), "text"),
    ]

    for target, name in targets:
        with pytest.raises(dataclasses.FrozenInstanceError):
            setattr(target, name, None)
    for start, end in ((-1, 2), (3, 3), (4, 1)):
        with pytest.raises(AppValueError):
            _ = Entity("ORG", start, end, "x")


def test_registry_refuses_unknown_tasks_and_round_trips_registered_ones() -> None:
    """Test that only registered tasks are accepted.

    A registered task's results pass the check.

    **Why this test is important:**
      - A consumer (an ``interview`` profile) adds tasks without an engine change; an
        unregistered name must fail loudly instead of returning nothing.

    **What it tests:**
      - a fresh registry knows only ``entities``; ``speaker`` raises ``INVALID_INPUT``
      - after ``register("speaker", Speaker)`` its results round-trip
        ``check_annotations``; registering it again is a no-op, with another type a
        ``CONFLICT``
    """
    registry = TaskRegistry()
    block = TextBlock("Ann: hello", "c1")
    annotation = Annotation(task_results={"speaker": (Speaker("Ann"),)})

    with pytest.raises(AppValueError) as unknown:
        _ = registry.get("speaker")
    registry.register("speaker", Speaker)
    registry.register("speaker", Speaker)
    checked = check_annotations([block], [annotation], registry)

    assert unknown.value.code is ErrorCode.INVALID_INPUT
    assert registry.get(ENTITIES) is Entity
    assert checked[0].task_results["speaker"] == (Speaker("Ann"),)
    with pytest.raises(ConflictError):
        registry.register("speaker", Entity)


def test_check_annotations_refuses_a_reply_that_does_not_match() -> None:
    """Test that a backend reply is checked against the blocks and the registry.

    **Why this test is important:**
      - A remote service's reply is untrusted input; an off-by-one span would tag the
        wrong words, a short reply would shift every block's entities onto its neighbour.

    **What it tests:**
      - a valid span passes; a span past the block's end, a span whose text differs, one
        annotation for two blocks, and a task result of the wrong type each raise
        ``INTERNAL``
    """
    registry = TaskRegistry()
    registry.register("speaker", Speaker)
    block = TextBlock("Acme Corp", "c1")
    good = Annotation(entities=(Entity("ORG", 0, 4, "Acme"),))
    bad = [
        ([block], [Annotation(entities=(Entity("ORG", 5, 12, "Corp..."),))]),
        ([block], [Annotation(entities=(Entity("ORG", 0, 4, "Acne"),))]),
        ([block, block], [good]),
        ([block], [Annotation(task_results={"speaker": (Entity("ORG", 0, 4, "Acme"),)})]),
    ]

    assert check_annotations([block], [good], registry) == [good]
    for blocks, reply in bad:
        with pytest.raises(AppError) as raised:
            _ = check_annotations(blocks, reply, registry)
        assert raised.value.code is ErrorCode.INTERNAL
