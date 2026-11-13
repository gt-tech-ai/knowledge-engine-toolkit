"""Tests for the ``markdown`` document splitter (one index unit per ``##`` section).

Why these tests matter: a section indexed without its document's title loses what it is
about, and frontmatter left in the text is embedded as noise while the metadata a search
filters on (type, status, tags) never reaches the index.
"""

from __future__ import annotations

from textwrap import dedent

from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.clients.splitting.markdown import MarkdownSplitter
from techai_webutils.core.domain import ChunkPayload, DocumentFormat, ParsedDocument

_DOC = dedent("""\
    ---
    type: Runbook
    tags: [db, pool]
    ---

    # Pool

    The connection pool.

    ## Sizing

    Set max_conns.

    ```sh
    ## not a heading inside a fence
    ```

    ## Draining

    Drain before deploy.
    """)


def _units(text: str) -> list[ChunkPayload]:
    """Split ``text`` as a Markdown document.

    Args:
        text: The document's text.

    Returns:
        The units the markdown splitter returns.

    """
    parsed = ParsedDocument(
        markdown_content=text, document_format=DocumentFormat.MD, source_path="pool.md"
    )
    return MarkdownSplitter().split(parsed)


def test_cuts_at_h2_and_prefixes_title() -> None:
    """Each ``##`` section is a unit that starts with the document's title.

    **Why this test is important:**
      - "Sizing" alone does not say it is about the pool; the title prefix keeps each
        section's embedding anchored to its document.

    **What it tests:**
      - Units: the preamble (title and intro), ``Sizing`` and ``Draining``; a ``##``
        inside a code fence does not cut; section units start ``# Pool``; line ranges
        point into the original file, frontmatter included.
    """
    units = _units(_DOC)

    assert [u.symbol for u in units] == ["", "Sizing", "Draining"]
    assert units[0].text.startswith("# Pool\n\nThe connection pool.")
    assert units[1].text.startswith("# Pool\n\n## Sizing\n")
    assert "## not a heading inside a fence" in units[1].text
    assert units[2].text == "# Pool\n\n## Draining\n\nDrain before deploy."
    assert [(u.line_start, u.line_end) for u in units] == [(6, 9), (10, 17), (18, 20)]
    assert units[1].section_path == ("Pool", "Sizing")


def test_frontmatter_is_stripped_and_returned() -> None:
    """Frontmatter leaves the text and comes back parsed on every unit.

    **Why this test is important:**
      - The caller lifts type, status and tags into the index metadata; in the text they
        would only add noise to the embedding.

    **What it tests:**
      - No unit's text contains the frontmatter; every unit carries
        ``{"type": "Runbook", "tags": ["db", "pool"]}``; a document whose frontmatter is
        not valid YAML keeps it as text and returns no metadata.
    """
    units = _units(_DOC)

    assert all("type: Runbook" not in u.text for u in units)
    assert all(
        u.frontmatter == {"type": "Runbook", "tags": ["db", "pool"]} for u in units
    )

    broken = _units("---\n: [unclosed\n---\n\n# T\n")
    assert broken[0].frontmatter == {}
    assert broken[0].text.startswith("---\n")


_MARKDOWN_LINES = ("", "# T", "## A", "## B", "```", "text", "---", "title: X", ": [")
"""Markdown lines, frontmatter and fences included, that the property test combines."""


@settings(max_examples=150, deadline=None)
@given(lines=st.lists(st.sampled_from(_MARKDOWN_LINES), max_size=14))
def test_units_cover_any_document(lines: list[str]) -> None:
    """Any document splits into ordered, in-bounds units, at least one.

    **Why this test is important:**
      - Wiki pages arrive half-written (an unclosed fence, a stray ``---``); the
        splitter must never raise or drop a document.

    **What it tests:**
      - At least one unit; indexes in order; line ranges ascending, disjoint and within
        the file.

    Args:
        lines: The document's lines.

    """
    units = _units("\n".join(lines))

    assert units
    assert [u.chunk_index for u in units] == list(range(len(units)))
    previous_end = 0
    for unit in units:
        assert unit.line_start is not None
        assert unit.line_end is not None
        assert previous_end < unit.line_start <= unit.line_end <= max(len(lines), 1)
        previous_end = unit.line_end
