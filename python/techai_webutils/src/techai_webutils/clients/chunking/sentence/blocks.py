"""``split_blocks``: a markdown text's typed blocks, which tile it.

Parsers emit markdown (``ParsedDocument.markdown_content``), so the sentence chunker reads
its
structure from it: ATX headings (``#`` to ``######``) set the section path; fenced code
(```` ``` ```` or ``~~~``) is one block up to its closing fence; a run of ``|`` lines
whose second
line is a separator row (``|---|``) is a pipe table; any other run of non-blank lines is a
paragraph. Blocks tile the text: blank lines belong to the block before them, and the
    first block
starts at 0. A table's caption is the paragraph right before it when that paragraph is one
short
line.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from enum import StrEnum

_HEADING = re.compile(r"^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$")
_FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
_TABLE_SEPARATOR = re.compile(r"^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$")
_CAPTION_MAX_CHARS = 200
"""The longest one-line paragraph read as the next table's caption."""


class BlockKind(StrEnum):
    """What a block holds; also each chunk's ``element_type``."""

    HEADING = "heading"
    """One ATX heading line."""
    PARAGRAPH = "paragraph"
    """Prose (and lists): split into sentences."""
    TABLE = "table"
    """A pipe table: split by rows, never mid-row."""
    CODE = "code"
    """A fenced code block: split by lines, never into sentences."""


@dataclass(frozen=True, slots=True)
class Block:
    """One typed block of a text, ``[start, end)``."""

    kind: BlockKind
    """What the block holds."""
    start: int
    """Offset of the block's first character (blank lines before it belong to the previous
    block)."""
    end: int
    """Offset one past the block's last character, trailing blank lines included."""
    section_path: tuple[str, ...]
    """The headings the block sits under, outermost first (a heading includes itself)."""
    caption: str = ""
    """A table's caption (the one-line paragraph before it); empty otherwise."""
    header_end: int = 0
    """A table's offset just past its header and separator rows; 0 otherwise."""


@dataclass(slots=True)
class _Line:
    """One line of the text with its offsets."""

    start: int
    end: int
    text: str


def _lines(text: str) -> list[_Line]:
    """Return ``text``'s lines (line endings kept in the offsets, not in ``text``).

    Returns:
        ``text``'s lines (line endings kept in the offsets, not in ``text``).

    """
    lines: list[_Line] = []
    offset = 0
    for raw in text.splitlines(keepends=True):
        lines.append(_Line(offset, offset + len(raw), raw.rstrip("\r\n")))
        offset += len(raw)
    return lines


def split_blocks(text: str) -> list[Block]:
    """Return ``text``'s blocks in order; they tile it (none for empty text).

    Returns:
        ``text``'s blocks in order; they tile it (none for empty text).

    """
    lines = _lines(text)
    found: list[tuple[BlockKind, int, int, tuple[str, ...], int]] = []
    path: list[tuple[int, str]] = []
    i = 0
    while i < len(lines):
        line = lines[i]
        if not line.text.strip():
            i += 1
            continue
        kind, last, header_end = _block_at(lines, i)
        if kind == BlockKind.HEADING:
            heading = _HEADING.match(line.text)
            level = len(heading.group(1)) if heading else 1
            while path and path[-1][0] >= level:
                path.pop()
            path.append((level, heading.group(2) if heading else line.text.strip()))
        found.append((
            kind,
            line.start,
            lines[last].end,
            tuple(t for _, t in path),
            header_end,
        ))
        i = last + 1
    if not found and text:
        return [Block(BlockKind.PARAGRAPH, 0, len(text), ())]
    return _tile(text, found)


def _block_at(lines: list[_Line], i: int) -> tuple[BlockKind, int, int]:
    """Classify the block starting at line ``i``; return its kind.

    Last line and header end.

    Returns:
        The block's kind, its last line, and a table's header-row end offset (else 0).

    """
    line = lines[i].text
    if _HEADING.match(line):
        return BlockKind.HEADING, i, 0
    fence = _FENCE.match(line)
    if fence:
        marker = fence.group(1)
        last = i + 1
        while last < len(lines) and not lines[last].text.lstrip().startswith(marker):
            last += 1
        return BlockKind.CODE, min(last, len(lines) - 1), 0
    if _is_table_start(lines, i):
        last = i + 1
        while last + 1 < len(lines) and lines[last + 1].text.lstrip().startswith("|"):
            last += 1
        return BlockKind.TABLE, last, lines[i + 1].end
    last = i
    while last + 1 < len(lines) and _continues_paragraph(lines, last + 1):
        last += 1
    return BlockKind.PARAGRAPH, last, 0


def _is_table_start(lines: list[_Line], i: int) -> bool:
    """Whether line ``i`` starts a pipe table.

    (a ``|`` row followed by a separator row).

    Returns:
        Whether line ``i`` starts a pipe table.

    """
    return (
        lines[i].text.lstrip().startswith("|")
        and i + 1 < len(lines)
        and bool(_TABLE_SEPARATOR.match(lines[i + 1].text))
    )


def _continues_paragraph(lines: list[_Line], i: int) -> bool:
    """Whether line ``i`` continues the paragraph above it.

    Returns:
        Whether line ``i`` continues the paragraph above it.

    """
    text = lines[i].text
    return bool(
        text.strip()
        and not _HEADING.match(text)
        and not _FENCE.match(text)
        and not _is_table_start(lines, i)
    )


def _tile(
    text: str, found: list[tuple[BlockKind, int, int, tuple[str, ...], int]]
) -> list[Block]:
    """Stretch the found blocks so they tile ``text``, and attach each table's caption.

    Returns:
        The blocks, stretched to tile ``text``.

    """
    blocks: list[Block] = []
    for n, (kind, start, _end, section_path, header_end) in enumerate(found):
        end = found[n + 1][1] if n + 1 < len(found) else len(text)
        caption = ""
        if kind == BlockKind.TABLE and blocks and blocks[-1].kind == BlockKind.PARAGRAPH:
            previous = text[blocks[-1].start : blocks[-1].end].strip()
            if "\n" not in previous and len(previous) <= _CAPTION_MAX_CHARS:
                caption = previous
        blocks.append(
            Block(kind, 0 if n == 0 else start, end, section_path, caption, header_end)
        )
    return blocks
