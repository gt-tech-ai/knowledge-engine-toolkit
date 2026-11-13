"""The grammars the ``code`` splitter knows, and how each names its declarations.

A ``Grammar`` maps a file extension to a tree-sitter language and to the node types that
are declarations, each with the function that names it. Everything here reads the parse
tree only; the splitter decides what becomes a unit.
"""

from __future__ import annotations

from collections.abc import Callable, Iterator, Mapping
from dataclasses import dataclass, field
from types import MappingProxyType

import tree_sitter_go
import tree_sitter_python
import tree_sitter_typescript
from tree_sitter import Language, Node

Namer = Callable[[Node], str]
"""Returns a declaration node's symbol (empty when it has no name)."""


@dataclass(frozen=True, slots=True)
class Grammar:
    """One language: its tree-sitter grammar and the declarations it splits at."""

    language: Language
    """The tree-sitter language the file parses with."""

    declarations: Mapping[str, Namer]
    """Declaration node types, top level or nested in a body, and how each is named."""

    wrappers: Mapping[str, str] = field(default_factory=dict[str, str])
    """Node types that wrap a declaration (a decorator, ``export``), and the field that
    holds it."""

    receiver: Callable[[Node], str] | None = None
    """Returns a method's receiver type name (Go), so the symbol reads ``Type.method``
    and the unit carries the type's declaration line; ``None`` when the language has no
    receivers."""

    type_rows: Callable[[Node], Iterator[tuple[str, int]]] | None = None
    """Yields each type a top-level node declares with its 0-based row, for the receiver
    line; ``None`` with ``receiver``."""

    def unwrap(self, node: Node) -> Node | None:
        """Return the declaration ``node`` is or wraps, else ``None``.

        Args:
            node: A top-level or body node.

        Returns:
            The innermost declaration node, or ``None`` when ``node`` is not one.

        """
        while node.type in self.wrappers:
            inner = node.child_by_field_name(self.wrappers[node.type])
            if inner is None:
                return None
            node = inner
        return node if node.type in self.declarations else None

    def symbol(self, node: Node) -> str | None:
        """Return the symbol of the declaration ``node`` is or wraps, else ``None``.

        Args:
            node: A top-level or body node.

        Returns:
            The declaration's symbol (``Type.method`` for a Go method), or ``None``.

        """
        inner = self.unwrap(node)
        if inner is None:
            return None
        name = self.declarations[inner.type](inner)
        if self.receiver is None:
            return name
        owner = self.receiver(inner)
        return f"{owner}.{name}" if owner else name


def _text(node: Node | None) -> str:
    """Return a node's source text, decoded (empty for ``None``).

    Args:
        node: A parse-tree node.

    Returns:
        Its UTF-8 text.

    """
    if node is None or node.text is None:
        return ""
    return node.text.decode("utf-8", errors="replace")


def _name(node: Node) -> str:
    """Return the text of a node's ``name`` field.

    Args:
        node: A declaration node.

    Returns:
        Its name, or empty.

    """
    return _text(node.child_by_field_name("name"))


def _spec_names(*spec_types: str) -> Namer:
    """Return a namer that joins the names of a grouped declaration's specs.

    Args:
        *spec_types: The spec node types (``const_spec``, ``variable_declarator``, ...).

    Returns:
        A namer giving ``A,B,C`` for ``const (A = 1; B, C = 2, 3)``.

    """

    def name(node: Node) -> str:
        names = [
            _text(ident)
            for spec in _descendants(node, spec_types, depth=2)
            for ident in spec.children_by_field_name("name")
        ]
        return ",".join(names)

    return name


def _descendants(node: Node, types: tuple[str, ...], depth: int) -> Iterator[Node]:
    """Yield the descendants of ``node`` of ``types``, at most ``depth`` levels down.

    Args:
        node: Where to start.
        types: The node types to yield.
        depth: How many levels to search.

    Yields:
        Each matching node, in source order.

    """
    for child in node.named_children:
        if child.type in types:
            yield child
        elif depth > 1:
            yield from _descendants(child, types, depth - 1)


def _go_receiver(node: Node) -> str:
    """Return a Go method's receiver type name (``Square`` for ``(s *Square[T])``).

    Args:
        node: A declaration node.

    Returns:
        The receiver type, or empty for a function.

    """
    if node.type != "method_declaration":
        return ""
    receiver = node.child_by_field_name("receiver")
    if receiver is None:
        return ""
    found = next(_descendants(receiver, ("type_identifier",), depth=4), None)
    return _text(found)


def _go_type_rows(node: Node) -> Iterator[tuple[str, int]]:
    """Yield each type a Go ``type`` declaration declares, with its spec's row.

    Args:
        node: A top-level node.

    Yields:
        ``(name, row)`` per type spec; nothing for other nodes.

    """
    if node.type != "type_declaration":
        return
    for spec in _descendants(node, ("type_spec", "type_alias"), depth=1):
        yield _name(spec), spec.start_point.row


_GO = Grammar(
    language=Language(tree_sitter_go.language()),
    declarations=MappingProxyType({
        "function_declaration": _name,
        "method_declaration": _name,
        "type_declaration": _spec_names("type_spec", "type_alias"),
        "const_declaration": _spec_names("const_spec"),
        "var_declaration": _spec_names("var_spec"),
    }),
    receiver=_go_receiver,
    type_rows=_go_type_rows,
)
"""Go: functions, methods (``Type.method``), and type/const/var declarations."""

_PYTHON = Grammar(
    language=Language(tree_sitter_python.language()),
    declarations=MappingProxyType({
        "function_definition": _name,
        "class_definition": _name,
    }),
    wrappers=MappingProxyType({"decorated_definition": "definition"}),
)
"""Python: functions and classes, decorators included."""

_TS_DECLARATIONS: Mapping[str, Namer] = MappingProxyType({
    "function_declaration": _name,
    "generator_function_declaration": _name,
    "class_declaration": _name,
    "abstract_class_declaration": _name,
    "interface_declaration": _name,
    "type_alias_declaration": _name,
    "enum_declaration": _name,
    "method_definition": _name,
    "lexical_declaration": _spec_names("variable_declarator"),
    "variable_declaration": _spec_names("variable_declarator"),
})
"""TypeScript declarations; ``method_definition`` only occurs nested in a class."""

_TS_WRAPPERS: Mapping[str, str] = MappingProxyType({"export_statement": "declaration"})
"""``export`` wraps a declaration in its ``declaration`` field."""

_TYPESCRIPT = Grammar(
    language=Language(tree_sitter_typescript.language_typescript()),
    declarations=_TS_DECLARATIONS,
    wrappers=_TS_WRAPPERS,
)
"""TypeScript (``.ts``)."""

_TSX = Grammar(
    language=Language(tree_sitter_typescript.language_tsx()),
    declarations=_TS_DECLARATIONS,
    wrappers=_TS_WRAPPERS,
)
"""TypeScript with JSX (``.tsx``)."""

GRAMMARS: Mapping[str, Grammar] = MappingProxyType({
    ".go": _GO,
    ".py": _PYTHON,
    ".ts": _TYPESCRIPT,
    ".tsx": _TSX,
})
"""The grammar for each file extension the ``code`` splitter parses."""
