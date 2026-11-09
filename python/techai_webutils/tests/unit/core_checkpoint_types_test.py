"""Tests for the checkpoint types (``core.interfaces.checkpoint``)."""

from __future__ import annotations

import dataclasses
from multiprocessing.reduction import ForkingPickler

import pytest

from techai_webutils.core.interfaces.checkpoint import CheckpointKey, Stage, StageArtifact


def _key(stage: Stage = Stage.PARSE) -> CheckpointKey:
    """Return a checkpoint key for ``stage`` of one document.

    Returns:
        A checkpoint key for ``stage`` of one document.

    """
    return CheckpointKey("org-1", "doc-1", "c0ffee", stage, "fp")


def test_stage_order_is_pipeline_order() -> None:
    """``Stage`` iterates in pipeline order.

    **Why this test is important:**
      - The resume planner walks stages in declaration order; a reordered enum would
        resume a document past a stage it never ran.

    **What it tests:**
      - ``list(Stage)`` is parse, enrich, chunk, embed, index.
    """
    assert [str(s) for s in Stage] == ["parse", "enrich", "chunk", "embed", "index"]


def test_keys_and_artifacts_are_frozen_hashable_and_picklable() -> None:
    """Keys and artifacts are immutable, hashable values that survive pickling.

    **Why this test is important:**
      - Keys index caches and sets, and artifacts cross to Ray workers by pickle; a
        mutable or unhashable key would corrupt a cache, and an unpicklable one would fail
        on Ray.

    **What it tests:**
      - Assigning any field of a key raises ``FrozenInstanceError``; equal keys hash
        equal; an artifact's hash ignores its ``versions`` mapping, and it round-trips
        through pickle.
    """
    key = _key()
    for field in dataclasses.fields(key):
        with pytest.raises(dataclasses.FrozenInstanceError):
            setattr(key, field.name, "other")
    assert {key, _key()} == {key}
    artifact = StageArtifact(
        key, b"{}", versions={"parser": "1.0"}, parts_done=frozenset({1})
    )
    other = StageArtifact(
        key, b"{}", versions={"parser": "2.0"}, parts_done=frozenset({1})
    )
    assert hash(artifact) == hash(other)
    assert artifact != other
    assert ForkingPickler.loads(ForkingPickler.dumps(artifact)) == artifact
