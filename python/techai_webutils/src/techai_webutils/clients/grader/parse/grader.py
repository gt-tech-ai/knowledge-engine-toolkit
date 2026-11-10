"""The ``parse`` grader: scores a parse's blocks against golden blocks.

One aspect at a time.

An eval case carries the golden blocks in ``expected["blocks"]`` and the system's output
carries the parse's blocks in ``answer``, both in the document IR's JSON form
(``blocks_to_json``). Each grader scores one ``ParseAspect`` in ``[0, 1]``:

- ``table_structure``: per golden table (matched to the parse's tables in order), the
  share of its row count, column count and header-cell positions the parse got right;
- ``cell_text``: per golden table, the share of its cells whose text the parse matches;
- ``reading_order``: the share of golden blocks found in the parse's text, times one minus
  the share of found pairs that appear out of order (inversions);
- ``heading_path``: the share of golden body blocks (not titles or headings) found in the
  parse under the same headings.

Text is compared normalized (lower case, punctuation and whitespace collapsed), so
markdown pipes and line breaks never count as differences. A parse that is not IR JSON
scores 0.
"""

from __future__ import annotations

import json
import re
from enum import StrEnum
from itertools import combinations
from typing import TYPE_CHECKING, cast, final, override

from techai_webutils.core.domain.document_ir import BlockType, blocks_from_json
from techai_webutils.core.errors import AppError, AppValueError, ErrorCode
from techai_webutils.core.interfaces.grader import Grader, GradeResult

if TYPE_CHECKING:
    from collections.abc import Callable, Sequence

    from techai_webutils.core.domain.document_ir import DocumentBlock
    from techai_webutils.core.interfaces.dataset import EvalCase
    from techai_webutils.core.interfaces.grader import EvalOutput

EXPECTED_BLOCKS_KEY = "blocks"
"""The eval case's ``expected`` key holding the golden blocks' IR JSON."""

_KEY_CHARS = 40
"""How many normalized characters of a block's text locate it in the parse."""

_NON_WORD = re.compile(r"[\W_]+")
"""A run of punctuation, markup or whitespace (collapsed to one space)."""

Score = tuple[float, str]
"""A score in ``[0, 1]`` and why."""


class ParseAspect(StrEnum):
    """Which property of a parse a ``ParseGrader`` scores."""

    TABLE_STRUCTURE = "table_structure"
    """Tables' row and column counts and header cells."""
    CELL_TEXT = "cell_text"
    """Tables' cell texts."""
    READING_ORDER = "reading_order"
    """Blocks found, and in the golden order."""
    HEADING_PATH = "heading_path"
    """Body blocks under the golden headings."""


def _norm(text: str) -> str:
    """Return ``text`` lower-cased with punctuation, markup and whitespace collapsed.

    Returns:
        ``text`` lower-cased with punctuation, markup and whitespace collapsed.

    """
    return _NON_WORD.sub(" ", text).strip().lower()


def _read(payload: str) -> tuple[DocumentBlock, ...]:
    """Parse IR JSON into blocks.

    (raises ``AppValueError`` or ``ValueError`` on bad input).

    Returns:
        The blocks read from the IR JSON.

    """
    return blocks_from_json(cast("dict[str, object]", json.loads(payload)))


def _tables(blocks: Sequence[DocumentBlock]) -> list[DocumentBlock]:
    """Return the table blocks, in reading order.

    Returns:
        The table blocks, in reading order.

    """
    return sorted((b for b in blocks if b.type is BlockType.TABLE), key=lambda b: b.order)


def _mean(values: Sequence[float]) -> float:
    """Return the mean of ``values`` (1.0 when there are none: nothing to get wrong).

    Returns:
        The mean of ``values`` (1.0 when there are none: nothing to get wrong).

    """
    return sum(values) / len(values) if values else 1.0


def _table_structure(
    golden: Sequence[DocumentBlock], parsed: Sequence[DocumentBlock]
) -> Score:
    """Score the parse's tables' row and column counts and header cells.

    Returns:
        The score and the rationale for it.

    """
    found = _tables(parsed)
    shares: list[float] = []
    for n, table in enumerate(_tables(golden)):
        other = found[n] if n < len(found) else None
        if other is None:
            shares.append(0.0)
            continue
        heads = {(c.row, c.col) for c in table.cells if c.header}
        other_heads = {(c.row, c.col) for c in other.cells if c.header}
        right = (table.rows == other.rows, table.cols == other.cols, heads == other_heads)
        shares.append(sum(right) / len(right))
    return _mean(shares), f"{len(found)} of {len(_tables(golden))} golden tables found"


def _cell_text(golden: Sequence[DocumentBlock], parsed: Sequence[DocumentBlock]) -> Score:
    """Score the share of each golden table's cells whose text the parse matches.

    Returns:
        The score and the rationale for it.

    """
    found = _tables(parsed)
    shares: list[float] = []
    for n, table in enumerate(_tables(golden)):
        texts = (
            {(c.row, c.col): _norm(c.text) for c in found[n].cells}
            if n < len(found)
            else {}
        )
        right = [texts.get((c.row, c.col)) == _norm(c.text) for c in table.cells]
        shares.append(_mean([float(r) for r in right]))
    return _mean(shares), f"cell text over {len(_tables(golden))} golden tables"


def _reading_order(
    golden: Sequence[DocumentBlock], parsed: Sequence[DocumentBlock]
) -> Score:
    """Score the golden blocks found in the parse's text, discounted by inversions.

    Returns:
        The score and the rationale for it.

    """
    text = " ".join(_norm(b.text) for b in sorted(parsed, key=lambda b: b.order))
    keys = [_norm(b.text)[:_KEY_CHARS] for b in sorted(golden, key=lambda b: b.order)]
    keys = [k for k in keys if k]
    positions = [p for p in (text.find(k) for k in keys) if p >= 0]
    pairs = list(combinations(positions, 2))
    inversions = sum(1 for a, b in pairs if a > b)
    order = 1.0 - inversions / len(pairs) if pairs else 1.0
    coverage = len(positions) / len(keys) if keys else 1.0
    return (
        coverage * order,
        f"{len(positions)}/{len(keys)} blocks found, {inversions} inversions",
    )


def _heading_path(
    golden: Sequence[DocumentBlock], parsed: Sequence[DocumentBlock]
) -> Score:
    """Score the share of golden body blocks found in the parse.

    Under the same headings.

    Returns:
        The score and the rationale for it.

    """
    body = [b for b in golden if b.type not in {BlockType.TITLE, BlockType.HEADING}]
    paths = [(_norm(b.text), tuple(_norm(h) for h in b.section_path)) for b in parsed]
    right = 0
    for block in body:
        key = _norm(block.text)[:_KEY_CHARS]
        want = tuple(_norm(h) for h in block.section_path)
        right += any(text.startswith(key) and path == want for text, path in paths)
    return _mean(
        [right / len(body)] if body else []
    ), f"{right}/{len(body)} under their headings"


_SCORERS: dict[
    ParseAspect, Callable[[Sequence[DocumentBlock], Sequence[DocumentBlock]], Score]
] = {
    ParseAspect.TABLE_STRUCTURE: _table_structure,
    ParseAspect.CELL_TEXT: _cell_text,
    ParseAspect.READING_ORDER: _reading_order,
    ParseAspect.HEADING_PATH: _heading_path,
}
"""The scorer of each aspect."""


@final
class ParseGrader(Grader):
    """Scores one ``ParseAspect`` of a parse against the case's golden blocks."""

    def __init__(self, aspect: ParseAspect, pass_score: float) -> None:
        """Bind the aspect and the score a parse must reach to pass.

        Args:
            aspect: What to score.
            pass_score: The bar in ``[0, 1]``; a score at or above it passes.

        """
        self._aspect = aspect
        self._pass = pass_score

    @override
    async def grade(self, case: EvalCase, output: EvalOutput) -> GradeResult:
        """Score ``output.answer``'s blocks against ``case.expected["blocks"]``.

        Returns:
            The grade for this case.

        Raises:
            AppError: ``INVALID_INPUT`` when the case's golden blocks are missing or
                unreadable (the eval set is wrong, not the parse).

        """
        name = f"parse_{self._aspect}"
        try:
            golden = _read(case.expected.get(EXPECTED_BLOCKS_KEY, ""))
        except (AppValueError, ValueError, KeyError) as exc:
            msg = f"eval case {case.id!r} has no readable golden blocks"
            raise AppError(ErrorCode.INVALID_INPUT, msg) from exc
        try:
            parsed = _read(output.answer)
        except (AppValueError, ValueError, KeyError):
            return GradeResult(
                name, 0.0, passed=False, rationale="the parse is not IR JSON"
            )
        score, why = _SCORERS[self._aspect](golden, parsed)
        return GradeResult(name, score, passed=score >= self._pass, rationale=why)
