"""Tests for the ``code`` document splitter (one index unit per top-level declaration).

Why these tests matter: an agent asking where something is implemented gets back the
unit a declaration was indexed as. A function split mid-body, a method without its
receiver type, or a wrong line range sends the citation to the wrong code.
"""

from __future__ import annotations

from textwrap import dedent

from hypothesis import given, settings
from hypothesis import strategies as st

from techai_webutils.clients.splitting.code import CodeSplitter
from techai_webutils.clients.splitting.kb_unit import split_into_chunks
from techai_webutils.core.domain import ChunkPayload, DocumentFormat, ParsedDocument

_BUDGET = 8192
"""A symbol budget no fixture declaration reaches."""

_FALLBACK_MAX_BYTES = 40
"""The fallback pack's ceiling: small enough that the fallback splits."""

_SMALL_BUDGET = 120
"""A symbol budget the oversize fixture's class exceeds."""

_GO = dedent("""\
    package shapes

    import "fmt"

    // Shape is anything with an area.
    type Shape interface {
    \tArea() float64
    }

    // Square is a square.
    type Square struct {
    \tSide float64
    }

    // Area returns the square's area.
    func (s *Square) Area() float64 {
    \treturn s.Side * s.Side
    }

    // Limits bound a side.
    const (
    \tMinSide = 1
    \tMaxSide = 10
    )

    // Describe prints a shape's area.
    func Describe(s Shape) {
    \tfmt.Println(s.Area())
    }
    """)

_PYTHON = dedent('''\
    """Circles."""

    import math


    def area(radius: float) -> float:
        """Return the circle's area."""
        return math.pi * radius**2


    # Circle is round.
    @dataclass
    class Circle:
        """A circle."""

        radius: float

        def area(self) -> float:
            return area(self.radius)
    ''')

_TYPESCRIPT = dedent("""\
    import { log } from "./log";

    /** Greeter greets. */
    export class Greeter {
      greet(name: string): string {
        return `hi ${name}`;
      }
    }

    export function add(a: number, b: number): number {
      return a + b;
    }

    export const LIMIT = 3;

    export interface Point {
      x: number;
    }

    type Id = string;
    """)

_OVERSIZE = dedent('''\
    class Big:
        """A class over the budget."""

        size = 1

        def one(self) -> int:
            return 1

        def two(self) -> int:
            return 2
    ''')


def _split(source: str, path: str, budget: int = _BUDGET) -> list[ChunkPayload]:
    """Split ``source`` as the file at ``path`` with a ``budget``-byte symbol budget.

    Args:
        source: The file's text.
        path: The file's path; its extension picks the grammar.
        budget: The largest declaration unit before it splits at nested blocks.

    Returns:
        The units the code splitter returns.

    """
    parsed = ParsedDocument(
        markdown_content=source, document_format=DocumentFormat.TXT, source_path=path
    )
    splitter = CodeSplitter(max_bytes=_FALLBACK_MAX_BYTES, symbol_max_bytes=budget)
    return splitter.split(parsed)


def _lines(source: str, unit: ChunkPayload) -> str:
    """Return the source lines ``unit`` says it covers.

    Args:
        source: The file's text.
        unit: A unit split from it.

    Returns:
        Lines ``line_start`` through ``line_end`` (1-based, inclusive), joined.

    """
    assert unit.line_start is not None
    assert unit.line_end is not None
    return "".join(source.splitlines(keepends=True)[unit.line_start - 1 : unit.line_end])


def test_go_declarations_and_receiver_line() -> None:
    """Go splits one unit per declaration, a method carrying its receiver's type line.

    **Why this test is important:**
      - A method read alone does not say which type it belongs to; the receiver type's
        declaration line is the context that makes ``Area`` findable as ``Square.Area``.

    **What it tests:**
      - The package clause and imports are one unit; each type, the const block, the
        function and the method are one unit each, doc comment included, in order.
      - The method unit is ``Square.Area``, starts with ``type Square struct {`` and
        then its own lines; every unit's lines match its range.
    """
    units = _split(_GO, "pkg/shapes/shapes.go")

    assert [u.symbol for u in units] == [
        "",
        "Shape",
        "Square",
        "Square.Area",
        "MinSide,MaxSide",
        "Describe",
    ]
    assert [u.chunk_index for u in units] == list(range(len(units)))
    method = units[3]
    assert method.text == "type Square struct {\n" + _lines(_GO, method).rstrip("\n")
    assert _lines(_GO, method).startswith("// Area returns the square's area.\n")
    for unit in (units[0], units[1], units[2], units[4], units[5]):
        assert unit.text == _lines(_GO, unit).rstrip("\n")
    assert all(u.element_type == "code" for u in units)


def test_python_functions_and_classes() -> None:
    """Python splits a function and a decorated class into one unit each.

    **Why this test is important:**
      - The decorator and the comment above a class belong to it; leaving them in a
        separate unit loses the context a reader needs.

    **What it tests:**
      - Units: the module docstring and import, ``area`` and ``Circle``; the class unit
        starts at its comment and keeps its method whole.
    """
    units = _split(_PYTHON, "lib/circle.py")

    assert [u.symbol for u in units] == ["", "area", "Circle"]
    circle = units[2]
    assert circle.text.startswith("# Circle is round.\n@dataclass\nclass Circle:")
    assert "def area(self)" in circle.text
    assert (circle.line_start, circle.line_end) == (11, 19)


def test_typescript_exports() -> None:
    """TypeScript splits exported and plain declarations by their names.

    **Why this test is important:**
      - Most TypeScript declarations sit inside ``export`` statements; a splitter that
        only knew bare declarations would index a module as one unit.

    **What it tests:**
      - The import, then ``Greeter`` (with its JSDoc), ``add``, ``LIMIT``, ``Point`` and
        ``Id``, one unit each.
    """
    units = _split(_TYPESCRIPT, "web/greeter.ts")

    assert [u.symbol for u in units] == ["", "Greeter", "add", "LIMIT", "Point", "Id"]
    assert units[1].text.startswith("/** Greeter greets. */\nexport class Greeter {")


def test_oversize_declaration_splits_at_nested_blocks() -> None:
    """A declaration over the budget splits into its nested definitions.

    **Why this test is important:**
      - One huge class indexed whole drowns its methods in a single embedding; split
        into members, each method stays findable and each unit keeps its class line.

    **What it tests:**
      - ``Big`` over a 120-byte budget becomes ``Big`` (header, docstring, field),
        ``Big.one`` and ``Big.two``; the member units start with ``class Big:``; the
        ranges tile the class's lines.
    """
    units = _split(_OVERSIZE, "big.py", budget=_SMALL_BUDGET)

    assert [u.symbol for u in units] == ["Big", "Big.one", "Big.two"]
    assert all(u.text.startswith("class Big:\n") for u in units)
    assert [(u.line_start, u.line_end) for u in units] == [(1, 5), (6, 8), (9, 10)]


def test_unknown_extension_falls_back_to_fixed() -> None:
    """A file with no grammar is packed by the ``kb_unit`` paragraph split.

    **Why this test is important:**
      - Repositories hold files no grammar covers (YAML, SQL, shell); they must still be
        indexed, at the size the fallback allows.

    **What it tests:**
      - A ``.sql`` file and a file with no path split exactly as ``split_into_chunks``
        at the configured ``max_bytes``.
    """
    source = "select 1;\n\nselect 2;\n\nselect 3;\n\nselect 4;\n"
    for path in ("db/query.sql", ""):
        parsed = ParsedDocument(
            markdown_content=source, document_format=DocumentFormat.TXT, source_path=path
        )
        expected = split_into_chunks(parsed, max_bytes=_FALLBACK_MAX_BYTES)
        assert len(expected) > 1
        assert _split(source, path) == expected


_FRAGMENTS = (
    "",
    "# note",
    "// note",
    "import os",
    "x = 1; y = 2",
    "def f(a):\n    return a\n",
    "@d\ndef g():\n    pass\n",
    "class C:\n    # m\n    def m(self):\n        pass\n    z = 3\n",
    "func F() {\n\treturn\n}",
    "func (r *R) M() {}",
    "type R struct{}",
    "export function h() { return 1; }",
    "def (",
    "}",
    "\u2028weird",
)
"""Source fragments, valid and broken, that the property tests combine."""


def _assert_tiles(source: str, units: list[ChunkPayload]) -> None:
    """Assert the units are ordered, in bounds, disjoint and cover every non-blank line.

    Args:
        source: The file's text.
        units: The units split from it.

    """
    rows = source.split("\n")
    assert units
    assert [u.chunk_index for u in units] == list(range(len(units)))
    covered: set[int] = set()
    previous_end = 0
    for unit in units:
        assert unit.line_start is not None
        assert unit.line_end is not None
        assert previous_end < unit.line_start <= unit.line_end <= len(rows)
        previous_end = unit.line_end
        covered.update(range(unit.line_start, unit.line_end + 1))
    blank = {n for n, row in enumerate(rows, start=1) if not row.strip()}
    assert set(range(1, len(rows) + 1)) - blank <= covered


@settings(max_examples=150, deadline=None)
@given(
    parts=st.lists(st.sampled_from(_FRAGMENTS), min_size=1, max_size=10),
    suffix=st.sampled_from([".py", ".go", ".ts", ".tsx"]),
    budget=st.integers(min_value=10, max_value=400),
)
def test_units_tile_any_source(parts: list[str], suffix: str, budget: int) -> None:
    """Any source, valid or broken, splits into ordered units covering its code.

    **Why this test is important:**
      - Repositories hold half-written and generated files; the splitter must never
        raise, drop a line of code or cite a line twice, whatever the parser makes of
        the text.

    **What it tests:**
      - For any mix of fragments, extension and budget: at least one unit, indexes in
        order, ranges ascending and disjoint within the file, every non-blank line in a
        unit.
    """
    source = "\n".join(parts)
    if not source.strip():
        return
    _assert_tiles(source, _split(source, f"f{suffix}", budget=budget))
