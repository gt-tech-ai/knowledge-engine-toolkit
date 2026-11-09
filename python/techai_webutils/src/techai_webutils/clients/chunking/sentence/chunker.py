"""``SentenceChunker``: token-sized chunks that end at sentence boundaries and keep tables whole.

The text's blocks (``split_blocks``) are grouped into runs: consecutive paragraphs under the same
section, each table, and each code block; a heading opens the run after it. A run's units are
sentences (paragraphs, from the injected ``SentenceSplitter``), rows (tables, after one header
unit) or lines (code). Units are packed greedily up to ``max_tokens``, counted by the injected
tokenizer (the embedding model's own), each unit counted once, so packing is linear in the text.
A unit over the budget is cut at whitespace (else mid-word) into pieces that fit.

Paragraph chunks repeat up to ``overlap_tokens`` of the previous chunk's last sentences. A table
too big for one chunk splits between rows, and each piece after the first repeats the header rows
(unless the header alone is over half the budget). Code is never split into sentences. Packing
assumes the tokenizer is subadditive (a text counts no more than its parts); word, character and
subword counts are, to within a token or two at each boundary.
"""

from __future__ import annotations

import bisect
import dataclasses
import itertools
from dataclasses import dataclass, field
from typing import TYPE_CHECKING

from techai_webutils.clients.chunking.sentence.blocks import (
    Block,
    BlockKind,
    split_blocks,
)
from techai_webutils.core.errors import AppValueError
from techai_webutils.core.interfaces.chunker import StructuredChunk

if TYPE_CHECKING:
    from collections.abc import Callable, Sequence

    from techai_webutils.core.domain import Turn
    from techai_webutils.core.interfaces.annotator import Entity
    from techai_webutils.core.interfaces.sentence_splitter import SentenceSplitter

_SHRINK = 0.9
"""Aim a cut piece at this share of the budget, so the first try usually fits."""


@dataclass(slots=True)
class _Unit:
    """One packable piece of a run, ``[start, end)``, and its token count."""

    start: int
    end: int
    tokens: int


@dataclass(slots=True)
class _Run:
    """Blocks packed together: one element type under one section."""

    kind: BlockKind
    section_path: tuple[str, ...]
    caption: str = ""
    header: str = ""
    header_tokens: int = 0
    units: list[_Unit] = field(default_factory=list)


class SentenceChunker:
    """The ``sentence`` chunker: a ``StructuredChunker`` over sentences, rows and lines."""

    def __init__(
        self,
        splitter: SentenceSplitter,
        tokenizer: Callable[[str], int],
        *,
        max_tokens: int,
        overlap_tokens: int,
        window_chars: int,
    ) -> None:
        """Pack chunks of at most ``max_tokens`` tokens, as ``tokenizer`` counts them.

        ``overlap_tokens`` (below ``max_tokens``) is the paragraph overlap; ``window_chars`` bounds
        the text passed to ``splitter`` in one call.

        Raises:
            AppValueError: ``max_tokens`` or ``window_chars`` below 1, or ``overlap_tokens``
                negative or not below ``max_tokens`` (``INVALID_INPUT``).

        """
        if max_tokens < 1 or window_chars < 1 or not 0 <= overlap_tokens < max_tokens:
            msg = (
                f"sentence chunker needs max_tokens >= 1, window_chars >= 1 and "
                f"0 <= overlap_tokens < max_tokens; got {max_tokens}, {window_chars}, "
                f"{overlap_tokens}"
            )
            raise AppValueError(msg)
        self._splitter = splitter
        self._tokens = tokenizer
        self._max = max_tokens
        self._overlap = overlap_tokens
        self._window = window_chars

    def chunk(self, text: str) -> list[str]:
        """Return the non-empty chunk texts, stripped (the ``Chunker`` contract)."""
        return [c.text.strip() for c in self.chunk_spans(text) if c.text.strip()]

    def chunk_spans(
        self,
        text: str,
        *,
        entities: Sequence[Entity] = (),
        turns: Sequence[Turn] = (),
        language: str | None = None,
    ) -> list[StructuredChunk]:
        """Return ``text``'s chunks with their spans, sections, types, entities and turns.

        Each chunk carries the ``entities`` it contains and the ``turns`` it overlaps.
        ``language`` picks the sentence rules; None uses the splitter's default.
        """
        chunks = [
            chunk for run in self._runs(text, language) for chunk in self._pack(text, run)
        ]
        return _attach_turns(_attach(chunks, entities), turns)

    def _runs(self, text: str, language: str | None) -> list[_Run]:
        """Group ``text``'s blocks into runs, each heading opening the run after it."""
        runs: list[_Run] = []
        headings: list[Block] = []
        current: _Run | None = None
        for block in split_blocks(text):
            if block.kind == BlockKind.HEADING:
                current = None
                headings.append(block)
                continue
            joins = (
                block.kind == BlockKind.PARAGRAPH
                and current is not None
                and current.kind == BlockKind.PARAGRAPH
                and current.section_path == block.section_path
                and not headings
            )
            if not joins or current is None:
                current = self._open(text, block, headings)
                runs.append(current)
                headings = []
            current.units.extend(self._block_units(text, block, current, language))
            if block.kind != BlockKind.PARAGRAPH:
                current = None
        if headings:
            run = _Run(BlockKind.HEADING, headings[-1].section_path)
            run.units = [
                u for h in headings for u in self._fit(text, h.start, h.end, self._max)
            ]
            runs.append(run)
        return runs

    def _open(self, text: str, block: Block, headings: list[Block]) -> _Run:
        """Start a run at ``block``, with the waiting headings as its first units."""
        run = _Run(block.kind, block.section_path, caption=block.caption)
        if block.kind == BlockKind.TABLE:
            run.header = text[block.start : block.header_end].lstrip("\r\n")
            run.header_tokens = self._tokens(run.header)
        budget = self._budget(run)
        run.units = [u for h in headings for u in self._fit(text, h.start, h.end, budget)]
        return run

    def _budget(self, run: _Run) -> int:
        """Return the tokens a unit of ``run`` may hold (room for a repeated table header)."""
        return self._max - run.header_tokens if self._repeats_header(run) else self._max

    def _repeats_header(self, run: _Run) -> bool:
        """Whether a table piece after the first repeats the header."""
        return run.kind == BlockKind.TABLE and 0 < run.header_tokens * 2 <= self._max

    def _block_units(
        self, text: str, block: Block, run: _Run, language: str | None
    ) -> list[_Unit]:
        """Return ``block``'s units: sentences, a table's header and rows, or code lines."""
        budget = self._budget(run)
        if block.kind == BlockKind.PARAGRAPH:
            units: list[_Unit] = []
            for start, end in _windows(text, block.start, block.end, self._window):
                for span in self._splitter.split(text[start:end], language=language):
                    units.extend(
                        self._fit(text, start + span.start, start + span.end, budget)
                    )
            return units
        units = []
        first_row = block.start
        if block.kind == BlockKind.TABLE:
            units.extend(self._fit(text, block.start, block.header_end, self._max))
            first_row = block.header_end
        for start, end in _line_spans(text, first_row, block.end):
            units.extend(self._fit(text, start, end, budget))
        return units

    def _fit(self, text: str, start: int, end: int, budget: int) -> list[_Unit]:
        """Return ``[start, end)`` as one unit, or cut into pieces of at most ``budget`` tokens."""
        tokens = self._tokens(text[start:end])
        if tokens <= budget:
            return [_Unit(start, end, tokens)]
        chars_per_token = (end - start) / tokens
        pieces: list[_Unit] = []
        position = start
        while position < end:
            cut = min(end, position + max(1, int(budget * chars_per_token * _SHRINK)))
            cut = _after_whitespace(text, position, cut, end)
            count = self._tokens(text[position:cut])
            while count > budget and cut - position > 1:
                cut = position + max(1, int((cut - position) * budget * _SHRINK / count))
                count = self._tokens(text[position:cut])
            pieces.append(_Unit(position, cut, count))
            position = cut
        return pieces

    def _pack(self, text: str, run: _Run) -> list[StructuredChunk]:
        """Pack ``run``'s units into chunks of at most ``max_tokens`` tokens."""
        chunks: list[StructuredChunk] = []
        current: list[_Unit] = []
        total = 0
        for unit in run.units:
            limit = self._max - (
                run.header_tokens if chunks and self._repeats_header(run) else 0
            )
            if current and total + unit.tokens > limit:
                chunks.append(self._emit(text, run, current, repeat=bool(chunks)))
                limit = self._max - (
                    run.header_tokens if self._repeats_header(run) else 0
                )
                current = self._carry(run, current, limit - unit.tokens)
                total = sum(u.tokens for u in current)
            current.append(unit)
            total += unit.tokens
        if current:
            chunks.append(self._emit(text, run, current, repeat=bool(chunks)))
        return chunks

    def _carry(self, run: _Run, previous: list[_Unit], room: int) -> list[_Unit]:
        """Return the previous chunk's last units to repeat: paragraphs only, within the overlap."""
        if run.kind != BlockKind.PARAGRAPH or self._overlap == 0:
            return []
        allowance = min(self._overlap, room)
        carried: list[_Unit] = []
        total = 0
        for unit in reversed(previous[1:]):
            if total + unit.tokens > allowance:
                break
            carried.append(unit)
            total += unit.tokens
        return carried[::-1]

    def _emit(
        self, text: str, run: _Run, units: list[_Unit], *, repeat: bool
    ) -> StructuredChunk:
        """Build the chunk covering ``units`` (a later table piece repeats the header first)."""
        start, end = units[0].start, units[-1].end
        prefix = run.header if repeat and self._repeats_header(run) else ""
        return StructuredChunk(
            text=prefix + text[start:end],
            start=start,
            end=end,
            section_path=run.section_path,
            element_type=str(run.kind),
            caption=run.caption,
        )


def _windows(text: str, start: int, end: int, size: int) -> list[tuple[int, int]]:
    """Cut ``[start, end)`` into consecutive windows of at most ``size`` chars, at whitespace."""
    windows: list[tuple[int, int]] = []
    position = start
    while position < end:
        cut = _after_whitespace(text, position, min(end, position + size), end)
        windows.append((position, cut))
        position = cut
    return windows


def _after_whitespace(text: str, start: int, cut: int, end: int) -> int:
    """Move ``cut`` back to just after the last whitespace in ``(start, cut)``, if any."""
    if cut >= end:
        return end
    found = max(text.rfind(" ", start + 1, cut), text.rfind("\n", start + 1, cut))
    return found + 1 if found > start else cut


def _line_spans(text: str, start: int, end: int) -> list[tuple[int, int]]:
    """Return the spans of the lines in ``[start, end)``, line endings included."""
    spans: list[tuple[int, int]] = []
    position = start
    for line in text[start:end].splitlines(keepends=True):
        spans.append((position, position + len(line)))
        position += len(line)
    return spans


def _attach(
    chunks: list[StructuredChunk], entities: Sequence[Entity]
) -> list[StructuredChunk]:
    """Give each chunk the entities wholly inside its span."""
    if not entities:
        return chunks
    ordered = sorted(entities, key=lambda e: (e.start, e.end))
    starts = [e.start for e in ordered]
    attached: list[StructuredChunk] = []
    for chunk in chunks:
        first = bisect.bisect_left(starts, chunk.start)
        last = bisect.bisect_left(starts, chunk.end)
        inside = tuple(e for e in ordered[first:last] if e.end <= chunk.end)
        attached.append(dataclasses.replace(chunk, entities=inside))
    return attached


def _attach_turns(
    chunks: list[StructuredChunk], turns: Sequence[Turn]
) -> list[StructuredChunk]:
    """Give each chunk the turns its span overlaps (a long turn spans several chunks).

    Turns are sorted by start; ``reach[i]`` is the furthest end among the first ``i + 1``, so
    every turn before the first ``reach`` past a chunk's start ends before the chunk, and the
    scan per chunk covers only the turns that can overlap it.
    """
    if not turns:
        return chunks
    ordered = sorted(turns, key=lambda t: (t.start, t.index))
    starts = [t.start for t in ordered]
    reach = list(itertools.accumulate((t.end for t in ordered), max))
    attached: list[StructuredChunk] = []
    for chunk in chunks:
        first = bisect.bisect_right(reach, chunk.start)
        last = bisect.bisect_left(starts, chunk.end)
        overlapping = tuple(t for t in ordered[first:last] if t.end > chunk.start)
        attached.append(dataclasses.replace(chunk, turns=overlapping))
    return attached
