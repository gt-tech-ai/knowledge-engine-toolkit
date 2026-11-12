"""The ``code`` document splitter: one index unit per top-level declaration.

A Go, Python or TypeScript file is parsed with tree-sitter and cut at its top-level
declarations, so a function, type or class is indexed whole with the comment above it.
Everything between declarations (package clause, imports, stray statements, nodes the
parser could not read) becomes a glue unit with an empty symbol. A Go method's unit starts
with its receiver type's declaration line, so the method reads with its type.

A declaration larger than ``symbol_max_bytes`` splits at its body: each nested definition
(a method, an inner function) becomes its own unit named ``Parent.child``, recursively,
and the other statements pack up to the budget. Every unit after the first starts with
the parent's header line. The units tile the declaration's lines.

A file with no grammar (or no path, or only whitespace) falls back to the ``kb_unit``
paragraph pack at ``max_bytes``.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import PurePosixPath
from typing import TYPE_CHECKING, final

from tree_sitter import Parser

from techai_webutils.clients.splitting.code.languages import GRAMMARS, Grammar
from techai_webutils.clients.splitting.kb_unit import split_into_chunks
from techai_webutils.core.domain import ChunkPayload

if TYPE_CHECKING:
    from tree_sitter import Node

    from techai_webutils.core.domain import ParsedDocument

_ELEMENT_TYPE = "code"
"""The ``element_type`` every unit this splitter makes carries."""


@dataclass(slots=True)
class _Draft:
    """A top-level unit being assembled: a declaration or a glue run."""

    symbol: str
    """The declaration's symbol; empty for glue."""
    node: Node | None
    """The declaration node (unwrapped); ``None`` for glue."""
    start: int
    """First 0-based row."""
    end: int
    """Last 0-based row."""


@dataclass(frozen=True, slots=True)
class _Piece:
    """One finished unit: its symbol, rows and the context lines before its text."""

    symbol: str
    """The declaration (``Parent.child``) or empty for glue."""
    start: int
    """First 0-based row."""
    end: int
    """Last 0-based row."""
    prefix: str
    """Context prepended to the unit's text (receiver or parent header lines)."""


def _last_row(node: Node) -> int:
    """Return the last row holding any of ``node``'s text.

    Args:
        node: A parse-tree node.

    Returns:
        The 0-based row; a node ending at column 0 ends on the row before.

    """
    end = node.end_point
    if end.column == 0 and end.row > node.start_point.row:
        return end.row - 1
    return end.row


@final
class _DraftBuilder:
    """Accumulates top-level drafts: pending comments, an open glue run, covered rows."""

    def __init__(self) -> None:
        """Start with no drafts, no pending comments and no row covered."""
        self.drafts: list[_Draft] = []
        self._glue_open = False
        self._covered = -1
        self._comments: list[tuple[int, int]] = []

    def extend_covered(self, start: int, end: int) -> bool:
        """Join a node that starts on a row the last draft already holds.

        Args:
            start: The node's first row.
            end: The node's last row.

        Returns:
            True when the node was joined to the last draft.

        """
        if not self.drafts or start > self._covered:
            return False
        self.drafts[-1].end = max(self.drafts[-1].end, end)
        self._covered = self.drafts[-1].end
        return True

    def comment(self, start: int, end: int) -> None:
        """Hold a comment; a blank line before it turns earlier pending comments to glue.

        Args:
            start: The comment's first row.
            end: The comment's last row.

        """
        if self._comments and start > self._comments[-1][1] + 1:
            self.flush_comments()
        self._comments.append((start, end))

    def glue(self, start: int, end: int) -> None:
        """Add rows to the open glue run, or open one.

        Args:
            start: First row.
            end: Last row.

        """
        if self._glue_open:
            self.drafts[-1].end = end
        else:
            self.drafts.append(_Draft("", None, start, end))
            self._glue_open = True
        self._covered = end

    def flush_comments(self) -> None:
        """Turn pending comments into glue."""
        if self._comments:
            self.glue(self._comments[0][0], self._comments[-1][1])
            self._comments.clear()

    def declaration(self, symbol: str, node: Node, start: int, end: int) -> None:
        """Add a declaration draft, with the comments directly above it.

        Args:
            symbol: The declaration's symbol.
            node: The unwrapped declaration node.
            start: Its first row.
            end: Its last row.

        """
        if self._comments and self._comments[-1][1] + 1 < start:
            self.flush_comments()
        first = self._comments[0][0] if self._comments else start
        self._comments.clear()
        self.drafts.append(_Draft(symbol, node, first, end))
        self._glue_open = False
        self._covered = end


@final
class _FileSplit:
    """Splits one parsed file; holds its lines, grammar and budget."""

    def __init__(self, grammar: Grammar, lines: list[str], budget: int) -> None:
        """Bind the file's grammar, its newline-split lines and the symbol budget.

        Args:
            grammar: The file's grammar.
            lines: The source split on newlines (tree-sitter's row boundary).
            budget: The largest declaration unit before it splits.

        """
        self._grammar = grammar
        self._lines = lines
        self._budget = budget

    def pieces(self, root: Node) -> list[_Piece]:
        """Return the file's units in source order.

        Args:
            root: The parse tree's root.

        Returns:
            The units; empty when the file has no nodes.

        """
        type_rows = self._type_rows(root)
        pieces: list[_Piece] = []
        for draft in self._drafts(root):
            if draft.node is None:
                pieces.append(_Piece("", draft.start, draft.end, ""))
                continue
            prefix = self._receiver_line(draft, type_rows)
            pieces.extend(
                self._declaration(
                    draft.node, draft.symbol, draft.start, draft.end, prefix
                )
            )
        return pieces

    def _drafts(self, root: Node) -> list[_Draft]:
        """Group the root's children into declaration and glue drafts.

        A run of comments directly above a declaration (no blank line between) joins it;
        any other comment is glue. A node that starts on a row an earlier unit already
        holds joins that unit, so units never share a row.

        Args:
            root: The parse tree's root.

        Returns:
            The drafts in source order.

        """
        builder = _DraftBuilder()
        for node in root.children:
            start, end = node.start_point.row, _last_row(node)
            if builder.extend_covered(start, end):
                continue
            if node.type == "comment":
                builder.comment(start, end)
                continue
            symbol = self._grammar.symbol(node)
            inner = self._grammar.unwrap(node)
            if symbol is None or inner is None:
                builder.flush_comments()
                builder.glue(start, end)
            else:
                builder.declaration(symbol, inner, start, end)
        builder.flush_comments()
        return builder.drafts

    def _type_rows(self, root: Node) -> dict[str, int]:
        """Return each top-level type's declaration row (Go receivers only).

        Args:
            root: The parse tree's root.

        Returns:
            Type name to 0-based row; empty for a language without receivers.

        """
        if self._grammar.type_rows is None:
            return {}
        rows: dict[str, int] = {}
        for node in root.children:
            rows.update(self._grammar.type_rows(node))
        return rows

    def _receiver_line(self, draft: _Draft, type_rows: dict[str, int]) -> str:
        """Return the receiver type's declaration line for a method, else empty.

        Args:
            draft: A declaration draft.
            type_rows: Each top-level type's row.

        Returns:
            The type's line plus a newline when it is in this file and outside the draft.

        """
        if self._grammar.receiver is None or draft.node is None:
            return ""
        row = type_rows.get(self._grammar.receiver(draft.node))
        if row is None or draft.start <= row <= draft.end:
            return ""
        return self._lines[row] + "\n"

    def _size(self, prefix: str, start: int, end: int) -> int:
        """Return the UTF-8 size of a unit's text.

        Args:
            prefix: The context before its lines.
            start: First 0-based row.
            end: Last 0-based row.

        Returns:
            Its size in bytes.

        """
        return len(prefix.encode("utf-8")) + len(
            "\n".join(self._lines[start : end + 1]).encode("utf-8")
        )

    def _declaration(
        self, node: Node, symbol: str, start: int, end: int, prefix: str
    ) -> list[_Piece]:
        """Return a declaration as one unit, or split at its body when over budget.

        Args:
            node: The (unwrapped) declaration node.
            symbol: Its symbol.
            start: First 0-based row, comments included.
            end: Last 0-based row.
            prefix: Context before its text (a receiver line, parent headers).

        Returns:
            Its units, tiling ``start`` to ``end``.

        """
        whole = [_Piece(symbol, start, end, prefix)]
        if self._size(prefix, start, end) <= self._budget:
            return whole
        body = node.child_by_field_name("body")
        if body is None:
            return whole
        starts = self._body_starts(body, symbol, start, prefix)
        if len(starts) == 1:
            return whole
        child_prefix = prefix + self._lines[node.start_point.row] + "\n"
        pieces: list[_Piece] = []
        for index, (piece_symbol, piece_start, nested) in enumerate(starts):
            piece_end = starts[index + 1][1] - 1 if index + 1 < len(starts) else end
            piece_prefix = prefix if index == 0 else child_prefix
            if nested is None:
                pieces.append(_Piece(piece_symbol, piece_start, piece_end, piece_prefix))
                continue
            pieces.extend(
                self._declaration(
                    nested, piece_symbol, piece_start, piece_end, piece_prefix
                )
            )
        return pieces

    def _body_starts(
        self, body: Node, symbol: str, start: int, prefix: str
    ) -> list[tuple[str, int, Node | None]]:
        """Return where each unit of an oversized declaration starts.

        Nested definitions start their own unit (with the comments directly above them);
        other statements pack until the next would pass the budget.

        Args:
            body: The declaration's body node.
            symbol: The declaration's symbol.
            start: The declaration's first row.
            prefix: Context before the declaration's text.

        Returns:
            ``(symbol, first row, nested declaration or None)`` per unit, in order.

        """
        starts: list[tuple[str, int, Node | None]] = [(symbol, start, None)]
        new_pack = False
        comment_start: int | None = None
        comment_end = -2
        for child in body.named_children:
            row = child.start_point.row
            if row <= starts[-1][1]:
                continue
            if child.type == "comment":
                if comment_start is None or row > comment_end + 1:
                    comment_start = row
                comment_end = _last_row(child)
                continue
            first = (
                comment_start
                if comment_start is not None and comment_end + 1 >= row
                else row
            )
            comment_start = None
            nested = self._grammar.unwrap(child)
            if nested is not None:
                name = self._grammar.declarations[nested.type](nested)
                starts.append((f"{symbol}.{name}", first, nested))
                new_pack = True
            elif (
                new_pack
                or self._size(prefix, starts[-1][1], _last_row(child)) > self._budget
            ):
                starts.append((symbol, first, None))
                new_pack = False
        return starts


@final
class CodeSplitter:
    """The ``code`` ``DocumentSplitter`` backend: one unit per top-level declaration.

    ``max_bytes`` is the fallback pack's ceiling for files with no grammar;
    ``symbol_max_bytes`` is the size a declaration unit may reach before it splits at its
    nested blocks.
    """

    def __init__(self, max_bytes: int, symbol_max_bytes: int) -> None:
        """Bind the fallback ceiling and the declaration budget.

        Args:
            max_bytes: The ``kb_unit`` fallback's largest chunk.
            symbol_max_bytes: The largest declaration unit before it splits.

        """
        self._max_bytes = max_bytes
        self._symbol_max_bytes = symbol_max_bytes

    def split(self, parsed: ParsedDocument) -> list[ChunkPayload]:
        """Return the file's units, at least one.

        Args:
            parsed: The file, its text in ``markdown_content`` and its path in
                ``source_path``.

        Returns:
            One unit per declaration and glue run, or the ``kb_unit`` fallback's chunks.

        """
        source = parsed.markdown_content
        grammar = GRAMMARS.get(PurePosixPath(parsed.source_path).suffix.lower())
        if grammar is None or not source.strip():
            return split_into_chunks(parsed, max_bytes=self._max_bytes)
        tree = Parser(grammar.language).parse(source.encode("utf-8"))
        lines = source.split("\n")
        pieces = _FileSplit(grammar, lines, self._symbol_max_bytes).pieces(tree.root_node)
        if not pieces:
            return split_into_chunks(parsed, max_bytes=self._max_bytes)
        offsets = [0]
        for line in lines:
            offsets.append(offsets[-1] + len(line) + 1)
        return [
            ChunkPayload(
                chunk_index=index,
                text=piece.prefix + "\n".join(lines[piece.start : piece.end + 1]),
                page_start=None,
                page_end=None,
                element_type=_ELEMENT_TYPE,
                span=(offsets[piece.start], min(offsets[piece.end + 1] - 1, len(source))),
                symbol=piece.symbol,
                line_start=piece.start + 1,
                line_end=piece.end + 1,
            )
            for index, piece in enumerate(pieces)
        ]
