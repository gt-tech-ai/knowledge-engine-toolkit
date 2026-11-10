"""Size budget for the package trees held to standard: function and file length, nesting.

Lengths count code lines only: docstring lines are left out, so documenting a symbol
never pushes it over budget (docs are never part of the code budget).
"""

import ast
from pathlib import Path

import techai_webutils

MAX_FUNC_LINES = 50
"""Longest function, from its ``def`` line to its last line, docstring lines left out."""
MAX_FILE_LINES = 800
"""Longest module, docstring lines left out."""
MAX_NESTING = 4
"""Deepest nesting of control blocks inside one function."""

PACKAGE = Path(techai_webutils.__file__).parent
BUDGET_ROOTS = (
    "clients/vector_kb",
    "clients/vector",
    "clients/retrieval",
    "clients/messaging",
    "clients/kb_ingestion",
    "clients/parsing",
    "clients/chunking",
    "clients/embedding",
    "clients/llm",
    "clients/storage",
    "execution",
    "core",
    "foundation/resources",
)
"""Trees held to the budget, relative to the package. A tree joins when a change first
brings it to the budget, and never leaves."""

_BLOCKS = (
    ast.If,
    ast.For,
    ast.AsyncFor,
    ast.While,
    ast.With,
    ast.AsyncWith,
    ast.Try,
    ast.Match,
)
_FUNCS = (ast.FunctionDef, ast.AsyncFunctionDef)


def _nesting(node: ast.AST, depth: int = 0) -> int:
    """Return the deepest control-block nesting under ``node``.

    A nested function counts on its own.

    Args:
        node: The node.
        depth: The depth so far.

    Returns:
        The deepest control-block nesting under ``node``.

    """
    deepest = depth
    for child in ast.iter_child_nodes(node):
        if isinstance(child, (*_FUNCS, ast.Lambda, ast.ClassDef)):
            continue
        inner = depth + 1 if isinstance(child, _BLOCKS) else depth
        deepest = max(deepest, _nesting(child, inner))
    return deepest


def _docstring_lines(tree: ast.Module) -> set[int]:
    """Return the line numbers the module's, classes' and functions' docstrings cover.

    Args:
        tree: The parsed module.

    Returns:
        Every line a docstring occupies.

    """
    lines: set[int] = set()
    for node in ast.walk(tree):
        if not isinstance(node, (ast.Module, ast.ClassDef, *_FUNCS)) or not node.body:
            continue
        first = node.body[0]
        if (
            isinstance(first, ast.Expr)
            and isinstance(first.value, ast.Constant)
            and isinstance(first.value.value, str)
        ):
            lines.update(range(first.lineno, (first.end_lineno or first.lineno) + 1))
    return lines


def size_violations(path: str, source: str) -> list[str]:
    """Return one line per function, nesting depth or file length over budget.

    Lengths leave docstring lines out.

    Args:
        path: The file, named in each violation.
        source: Its source.

    Returns:
        One line per violation; empty when ``source`` is within budget.

    """
    out: list[str] = []
    tree = ast.parse(source)
    docs = _docstring_lines(tree)
    lines = len(source.splitlines()) - len(docs)
    if lines > MAX_FILE_LINES:
        out.append(f"{path}: {lines} lines (max {MAX_FILE_LINES})")
    for node in ast.walk(tree):
        if not isinstance(node, _FUNCS):
            continue
        end = node.end_lineno or node.lineno
        span = sum(1 for n in range(node.lineno, end + 1) if n not in docs)
        if span > MAX_FUNC_LINES:
            out.append(f"{path}: {node.name} is {span} lines (max {MAX_FUNC_LINES})")
        depth = _nesting(node)
        if depth > MAX_NESTING:
            out.append(f"{path}: {node.name} nests {depth} deep (max {MAX_NESTING})")
    return out


def test_budgeted_trees_stay_within_budget() -> None:
    """Test that the budgeted trees hold no over-long function or module.

    Nor deep nesting.

    **Why this test is important:**
      - Long functions and deep nesting are where these trees' defects hid; the budget
        keeps a tree that was brought to standard from drifting back.

    **What it tests:**
      - Every module under ``BUDGET_ROOTS`` has functions of at most 50 lines, nesting of
        at most 4 and at most 800 lines, and every root exists.
    """
    violations: list[str] = []
    for root in BUDGET_ROOTS:
        base = PACKAGE / root
        assert base.is_dir(), f"budget root {root} does not exist"
        for module in sorted(base.rglob("*.py")):
            rel = module.relative_to(PACKAGE).as_posix()
            violations += size_violations(rel, module.read_text(encoding="utf-8"))

    assert violations == []


def test_an_over_budget_function_is_reported() -> None:
    """Test that the budget check reports what it claims to catch.

    **Why this test is important:**
      - A budget test that cannot fail guards nothing; this pins how it measures.

    **What it tests:**
      - A 51-line function and five nested blocks are reported; a 50-line function and
        four nested blocks are not.
      - A 50-line function with a long docstring is not: docstring lines are left out.
    """
    body = "    x = 1\n"
    docstring = '    """Do it.\n\n' + "    More.\n" * 20 + '    """\n'
    source = (
        "def long():\n" + body * 50 + "\n"
        "def fits():\n" + body * 49 + "\n"
        "def documented():\n" + docstring + body * 49 + "\n"
        "def deep():\n"
        + "".join("    " * (i + 1) + "if x:\n" for i in range(5))
        + "    " * 6
        + "pass\n\n"
        "def four():\n"
        + "".join("    " * (i + 1) + "if x:\n" for i in range(4))
        + "    " * 5
        + "pass\n"
    )

    assert size_violations("m.py", source) == [
        "m.py: long is 51 lines (max 50)",
        "m.py: deep nests 5 deep (max 4)",
    ]
