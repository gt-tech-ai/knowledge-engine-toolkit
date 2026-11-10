"""Unit tests for the parse-quality graders and the grader factory.

The graders are pure: they compare a golden block list with a parse's, both in the
document IR's JSON form, so no double is needed.
"""

from __future__ import annotations

import json

import pytest

from techai_webutils.clients.grader import GraderConfig, GraderKind, grader_from_config
from techai_webutils.core.domain.document_ir import (
    BlockType,
    DocumentBlock,
    TableCell,
    blocks_to_json,
)
from techai_webutils.core.errors import AppError
from techai_webutils.core.interfaces.dataset import EvalCase
from techai_webutils.core.interfaces.grader import EvalOutput

_TABLE_TEXT = "| region | total |\n|---|---|\n| EMEA | 10 |"
"""The table's markdown rendering."""


def _table(*, header: bool, total: str, grid: int = 2) -> DocumentBlock:
    """Return the sales table: a header row over one EMEA row whose total is ``total``.

    ``grid`` is the row and column count the table claims (2 is right).

    Args:
        header: Whether the table has a header row.
        total: The total row's label.
        grid: The rows and columns of scores.

    Returns:
        The sales table: a header row over one EMEA row whose total is ``total``.

    """
    return DocumentBlock(
        BlockType.TABLE,
        _TABLE_TEXT,
        4,
        section_path=("Report", "Sales"),
        cells=(
            TableCell(0, 0, "region", header=header),
            TableCell(0, 1, "total", header=header),
            TableCell(1, 0, "EMEA"),
            TableCell(1, 1, total),
        ),
        rows=grid,
        cols=grid,
    )


_GOLDEN = (
    DocumentBlock(BlockType.TITLE, "Report", 0),
    DocumentBlock(
        BlockType.PARAGRAPH, "Alpha paragraph text here.", 1, section_path=("Report",)
    ),
    DocumentBlock(
        BlockType.PARAGRAPH, "Beta paragraph text here.", 2, section_path=("Report",)
    ),
    DocumentBlock(
        BlockType.HEADING, "Sales", 3, section_path=("Report", "Sales"), level=2
    ),
    _table(header=True, total="10"),
)
"""A title, two paragraphs, a heading and a 2x2 table, in reading order."""

_BROKEN = (
    DocumentBlock(BlockType.TITLE, "Report", 0),
    DocumentBlock(
        BlockType.PARAGRAPH, "Beta paragraph text here.", 1, section_path=("Report",)
    ),
    DocumentBlock(BlockType.PARAGRAPH, "Alpha paragraph text here.", 2),
    DocumentBlock(
        BlockType.HEADING, "Sales", 3, section_path=("Report", "Sales"), level=2
    ),
    _table(header=False, total="16", grid=3),
)
"""The golden parse with the paragraphs swapped, Alpha's heading lost, and a table read as
3x3 with its header row unmarked and one cell misread."""

_ASPECTS = ("table_structure", "cell_text", "reading_order", "heading_path")
"""Every parse aspect."""


def _case() -> EvalCase:
    """Return the eval case whose expected blocks are the golden ones.

    Returns:
        The eval case whose expected blocks are the golden ones.

    """
    return EvalCase(
        id="golden-pdf",
        inputs={"path": "report.pdf"},
        expected={"blocks": json.dumps(blocks_to_json(_GOLDEN))},
        metadata={"format": "pdf"},
    )


async def _scores(
    blocks: tuple[DocumentBlock, ...] | str,
) -> dict[str, tuple[float, bool]]:
    """Grade ``blocks`` (or a raw answer) on every aspect; return each score and verdict.

    Args:
        blocks: The parsed blocks.

    Returns:
        The score and pass flag by aspect.

    """
    answer = blocks if isinstance(blocks, str) else json.dumps(blocks_to_json(blocks))
    out: dict[str, tuple[float, bool]] = {}
    for aspect in _ASPECTS:
        grader = grader_from_config(GraderConfig(kind=GraderKind.PARSE, aspect=aspect))
        result = await grader.grade(_case(), EvalOutput(answer, (), ""))
        assert result.grader == f"parse_{aspect}"
        out[aspect] = (round(result.score, 4), result.passed)
    return out


@pytest.mark.asyncio
async def test_parse_grader_scores_table_and_reading_order() -> None:
    """Test that the parse graders score tables, order and headings.

    Against golden blocks.

    **Why this test is important:**
      - A parser change that drops a table's header row, misreads a cell, reads two
        columns out of order or loses a heading degrades retrieval silently; the scores
        are what make such a regression visible beside the retrieval metrics.

    **What it tests:**
      - A parse equal to the golden blocks scores 1.0 and passes on every aspect.
      - The broken parse scores: table structure 0 (row count, column count and header
        cells all wrong), cell text 3/4 (one cell misread), reading order 0.9 (one
        inversion among five blocks' ten pairs; passes the 0.9 bar), heading path 2/3
        (Alpha lost its heading).
      - A parse that lost the Beta paragraph and the table scores reading order 0.6 (three
        of five blocks found, in order), heading path 1/3, and 0 on both table aspects.
      - An answer that is not IR JSON scores 0 and fails.
    """
    perfect = await _scores(_GOLDEN)
    broken = await _scores(_BROKEN)
    lossy = await _scores(_GOLDEN[:2] + _GOLDEN[3:4])
    garbage = await _scores("not json")

    assert perfect == dict.fromkeys(_ASPECTS, (1.0, True))
    assert broken == {
        "table_structure": (0.0, False),
        "cell_text": (0.75, False),
        "reading_order": (0.9, True),
        "heading_path": (0.6667, False),
    }
    assert lossy == {
        "table_structure": (0.0, False),
        "cell_text": (0.0, False),
        "reading_order": (0.6, False),
        "heading_path": (0.3333, False),
    }
    assert garbage == dict.fromkeys(_ASPECTS, (0.0, False))


def test_grader_factory_refuses_unknown_kind_and_aspect() -> None:
    """Test that the grader factory fails loudly on an unknown kind or aspect.

    An aspect is a parse aspect.

    **Why this test is important:**
      - A typo in an eval config must stop the run, not silently grade with the
        always-pass stub or skip an aspect.

    **What it tests:**
      - An unknown kind, a ``parse`` grader with no or an unknown aspect, and a pass score
        outside [0, 1] each raise a coded ``AppError``; the default config builds the
        stub.
    """
    assert grader_from_config(GraderConfig()) is not None
    for config in (
        GraderConfig(kind="llm"),
        GraderConfig(kind=GraderKind.PARSE),
        GraderConfig(kind=GraderKind.PARSE, aspect="fonts"),
    ):
        with pytest.raises(AppError):
            _ = grader_from_config(config)
    with pytest.raises(AppError):
        _ = GraderConfig(kind=GraderKind.PARSE, aspect="cell_text", pass_score=1.5)
