"""Size budget for the package trees held to standard: function length, file length.

Nesting.
"""

import ast
from pathlib import Path

import techai_webutils

MAX_FUNC_LINES = 50
"""Longest function, from its ``def`` line to its last line."""
MAX_FILE_LINES = 800
"""Longest module."""
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


def size_violations(path: str, source: str) -> list[str]:
    """Return one line per function.

    Nesting depth or file length in ``source`` over budget.

    Returns:
        One line per function.

    """
    out: list[str] = []
    lines = len(source.splitlines())
    if lines > MAX_FILE_LINES:
        out.append(f"{path}: {lines} lines (max {MAX_FILE_LINES})")
    for node in ast.walk(ast.parse(source)):
        if not isinstance(node, _FUNCS):
            continue
        span = (node.end_lineno or node.lineno) - node.lineno + 1
        if span > MAX_FUNC_LINES:
            out.append(f"{path}: {node.name} is {span} lines (max {MAX_FUNC_LINES})")
        depth = _nesting(node)
        if depth > MAX_NESTING:
            out.append(f"{path}: {node.name} nests {depth} deep (max {MAX_NESTING})")
    return out


def test_budgeted_trees_stay_within_budget() -> None:
    """Test that the budgeted trees hold no over-long function.

    Deep nesting or long module.

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
    """
    body = "    x = 1\n"
    source = (
        "def long():\n" + body * 50 + "\n"
        "def fits():\n" + body * 49 + "\n"
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
