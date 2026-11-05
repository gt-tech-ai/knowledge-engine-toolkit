"""DocumentParser stage contract: parse document bytes (or a file path) into a ``ParsedDocument``.

The shared, dependency-free parse seam every deployable programs against (mirrors Go's
``go/core/interfaces``). The config-selected backends in ``techai_webutils.clients.parsing``
satisfy it; a parser never raises for a bad document — it returns a ``ParsedDocument`` carrying the
error.

Imported by full submodule path (``techai_webutils.core.interfaces.parser``), NOT re-exported from
``core.interfaces`` — the package already exports a legacy, unrelated ``DocumentParser`` (see
``core.interfaces.document``); re-exporting this one would shadow it.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import ParsedDocument


@dataclass(frozen=True, slots=True)
class ParseCost:
    """What parsing one document costs the process: peak memory and CPUs."""

    memory_bytes: int
    """Peak memory one parse may use (the isolated parser's per-parse cap)."""
    cpu: float
    """CPUs one parse keeps busy."""


@runtime_checkable
class CostedParser(Protocol):
    """A parser that declares its per-document cost, so lanes and Ray tasks size from it.

    A capability beside ``DocumentParser`` (a Protocol, so a default method would not reach
    structural implementers): a consumer checks ``isinstance(parser, CostedParser)`` and falls back
    to its lane's configured parser memory when a parser does not implement it.
    """

    def cost(self) -> ParseCost:
        """Return the cost of parsing one document."""
        ...


@runtime_checkable
class DocumentParser(Protocol):
    """Parses raw document bytes or a file path into a ``ParsedDocument`` (never raises for a bad doc)."""

    async def parse(
        self, content: bytes, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse ``content`` given the declared type + filename (errors carried in the result)."""
        ...

    async def parse_path(
        self, path: str, *, declared: str = "", filename: str = ""
    ) -> ParsedDocument:
        """Parse a document from a file ``path`` (bounded memory — the large-document lane)."""
        ...
