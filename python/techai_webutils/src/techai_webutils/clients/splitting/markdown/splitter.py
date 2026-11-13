"""The ``markdown`` document splitter: one index unit per ``##`` section.

A leading ``---`` YAML frontmatter block is parsed and stripped; every unit carries the
parsed mapping so the caller can lift type, status and tags into index metadata. Text
that is not a YAML mapping stays in the document as text.

The body is cut at ``##`` headings outside code fences. The text before the first one
(the title and introduction) is its own unit. Each section unit starts with ``# <title>``
so it embeds with its document's subject; the title is the frontmatter's ``title``, else
the first ``#`` heading. Line ranges refer to the original file, frontmatter included.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import TYPE_CHECKING, cast, final

import yaml

from techai_webutils.core.domain import ChunkPayload

if TYPE_CHECKING:
    from techai_webutils.core.domain import ParsedDocument

_ELEMENT_TYPE = "section"
"""The ``element_type`` every unit this splitter makes carries."""

_FRONTMATTER_FENCE = "---"
"""The line that opens and closes a frontmatter block."""

_FENCES = ("```", "~~~")
"""Code-fence openers; a heading inside a fence does not cut."""

_H1 = re.compile(r"^# +(.+?)\s*#*\s*$")
"""A level-1 ATX heading."""

_H2 = re.compile(r"^## +(.+?)\s*#*\s*$")
"""A level-2 ATX heading: where a section starts."""


@dataclass(frozen=True, slots=True)
class _Section:
    """One unit before rendering: its heading and its 0-based row range."""

    heading: str
    """The ``##`` heading text; empty for the preamble."""
    start: int
    """First 0-based row."""
    end: int
    """Last 0-based row."""


def _frontmatter(lines: list[str]) -> tuple[dict[str, object], int]:
    """Parse a leading frontmatter block.

    Args:
        lines: The document split on newlines.

    Returns:
        The parsed mapping (empty when there is none) and the first body row. A block
        that is not a YAML mapping (or empty) is left in the body.

    """
    if not lines or lines[0].rstrip() != _FRONTMATTER_FENCE:
        return {}, 0
    close = next(
        (i for i in range(1, len(lines)) if lines[i].rstrip() == _FRONTMATTER_FENCE), None
    )
    if close is None:
        return {}, 0
    try:
        loaded = cast("object", yaml.safe_load("\n".join(lines[1:close])))
    except yaml.YAMLError:
        return {}, 0
    if loaded is None:
        return {}, close + 1
    if not isinstance(loaded, dict):
        return {}, 0
    mapping = cast("dict[object, object]", loaded)
    return {str(key): value for key, value in mapping.items()}, close + 1


def _sections(lines: list[str], body: int) -> tuple[list[_Section], str]:
    """Cut the body at ``##`` headings outside code fences.

    Args:
        lines: The document split on newlines.
        body: The first row after the frontmatter.

    Returns:
        The non-blank sections in order, and the first ``#`` heading (empty if none).

    """
    starts: list[tuple[str, int]] = [("", body)]
    title = ""
    fenced = False
    for row in range(body, len(lines)):
        line = lines[row]
        if line.lstrip(" ").startswith(_FENCES):
            fenced = not fenced
            continue
        if fenced:
            continue
        if heading := _H2.match(line):
            starts.append((heading.group(1), row))
        elif not title and (h1 := _H1.match(line)):
            title = h1.group(1)
    sections: list[_Section] = []
    for index, (heading, start) in enumerate(starts):
        end = starts[index + 1][1] - 1 if index + 1 < len(starts) else len(lines) - 1
        first = next((r for r in range(start, end + 1) if lines[r].strip()), None)
        if first is not None:
            sections.append(_Section(heading, first, end))
    return sections, title


@final
class MarkdownSplitter:
    """The ``markdown`` ``DocumentSplitter`` backend: one unit per ``##`` section."""

    @staticmethod
    def split(parsed: ParsedDocument) -> list[ChunkPayload]:
        """Return the document's units, at least one.

        Args:
            parsed: The document, its text in ``markdown_content``.

        Returns:
            The preamble and one unit per ``##`` section, each with the frontmatter.

        """
        text = parsed.markdown_content
        lines = text.split("\n")
        if len(lines) > 1 and not lines[-1]:
            _ = lines.pop()  # the text's final newline ends a line; it does not start one
        frontmatter, body = _frontmatter(lines)
        sections, h1 = _sections(lines, body)
        title_value = frontmatter.get("title")
        title = title_value if isinstance(title_value, str) and title_value else h1
        if not sections:
            return [
                ChunkPayload(
                    chunk_index=0,
                    text=text.strip("\n"),
                    page_start=None,
                    page_end=None,
                    element_type=_ELEMENT_TYPE,
                    span=(0, len(text)),
                    line_start=1,
                    line_end=len(lines),
                    frontmatter=frontmatter,
                )
            ]
        offsets = [0]
        for line in lines:
            offsets.append(offsets[-1] + len(line) + 1)
        heading_line = f"# {title}\n\n" if title else ""
        units: list[ChunkPayload] = []
        for index, section in enumerate(sections):
            body_text = "\n".join(lines[section.start : section.end + 1]).strip("\n")
            # The preamble already holds the ``#`` heading unless the title came only
            # from the frontmatter.
            prefixed = bool(section.heading) or not h1
            path = (title, section.heading) if section.heading else (title,)
            units.append(
                ChunkPayload(
                    chunk_index=index,
                    text=(heading_line if prefixed else "") + body_text,
                    page_start=None,
                    page_end=None,
                    section_path=tuple(part for part in path if part),
                    element_type=_ELEMENT_TYPE,
                    span=(offsets[section.start], offsets[section.end + 1] - 1),
                    symbol=section.heading,
                    line_start=section.start + 1,
                    line_end=section.end + 1,
                    frontmatter=frontmatter,
                )
            )
        return units
