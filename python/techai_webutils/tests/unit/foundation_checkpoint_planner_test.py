"""Tests for ``ResumePlanner``.

The golden cases in ``testdata/resume_planner/`` and a property.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime
from pathlib import Path
from typing import cast

import pytest
from hypothesis import given
from hypothesis import strategies as st

from techai_webutils.core.interfaces.checkpoint import Stage, StageState
from techai_webutils.foundation.checkpoint import SINK_BEDROCK, ResumePlanner

_EXPECTED_CASES_COUNT = 7

_GOLDEN = Path(__file__).resolve().parents[4] / "testdata" / "resume_planner"
_CASES = sorted(_GOLDEN.glob("*.json"))


def _state(raw: dict[str, object]) -> StageState:
    """Build a ``StageState`` from a golden case's JSON record.

    Args:
        raw: The stored state.

    Returns:
        A ``StageState`` from a golden case's JSON record.

    """
    return StageState(
        org_id=str(raw["org_id"]),
        document_id=str(raw["document_id"]),
        stage=Stage(str(raw["stage"])),
        content_hash=str(raw["content_hash"]),
        fingerprint=str(raw["fingerprint"]),
        artifact_key=str(raw["artifact_key"]),
        updated_at=datetime.fromisoformat(str(raw["updated_at"])),
        attempts=int(str(raw["attempts"])),
        shard=int(str(raw["shard"])),
    )


def test_golden_cases_exist() -> None:
    """The golden directory holds the cases the Go port replays.

    **Why this test is important:**
      - The parametrized test below passes vacuously on an empty directory; a moved or
        deleted fixture set must fail loudly.

    **What it tests:**
      - At least the seven committed cases are present.
    """
    assert len(_CASES) >= _EXPECTED_CASES_COUNT


@pytest.mark.parametrize("path", _CASES, ids=[case.stem for case in _CASES])
def test_golden_case(path: Path) -> None:
    """Each golden case resumes at its expected stage.

    **Why this test is important:**
      - These files are the contract a port of the planner replays; the Python planner
        must pass every one, or the two implementations would resume documents
        differently.

    **What it tests:**
      - ``plan(states, current, sink=...)`` returns the case's ``expected`` stage
        (``None`` for a fully indexed document): no states → parse; parse and enrich
        current → chunk; a stale chunk → chunk; Bedrock after chunk → index; a stale parse
        → parse.

    Args:
        path: The golden case file.

    """
    case = cast("dict[str, object]", json.loads(path.read_text(encoding="utf-8")))
    states = [
        _state(cast("dict[str, object]", raw))
        for raw in cast("list[object]", case["states"])
    ]
    current = {Stage(k): v for k, v in cast("dict[str, str]", case["current"]).items()}

    got = ResumePlanner().plan(states, current, sink=cast("str", case["sink"]))

    assert (None if got is None else str(got)) == case["expected"]


@given(
    valid=st.lists(st.booleans(), min_size=5, max_size=5),
    sink=st.sampled_from(["vector", SINK_BEDROCK]),
)
def test_never_skips_a_stage_with_an_invalid_upstream(
    valid: list[bool], sink: str
) -> None:
    """The plan never resumes past a stage whose own record is missing or stale.

    **Why this test is important:**
      - Resuming after a stage that must re-run would build on outdated output, indexing
        chunks of an old parse.

    **What it tests:**
      - For any pattern of current and stale records, every stage before the planned one
        is current (or skipped for the sink), and ``None`` only when every run stage is
        current.

    Args:
        valid: Which upstream stages are valid.
        sink: The sink's state.

    """
    current = {stage: f"fp-{stage}" for stage in Stage}
    stamp = datetime(2026, 11, 5, tzinfo=UTC)
    states = [
        StageState("o", "d", stage, "h", current[stage] if ok else "stale", "k", stamp, 1)
        for stage, ok in zip(Stage, valid, strict=True)
    ]
    run = [s for s in Stage if not (s is Stage.EMBED and sink == SINK_BEDROCK)]
    ok_by_stage = dict(zip(Stage, valid, strict=True))

    got = ResumePlanner().plan(states, current, sink=sink)

    before = run if got is None else run[: run.index(got)]
    assert all(ok_by_stage[s] for s in before)
    if got is not None:
        assert not ok_by_stage[got]
