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

from typing import TYPE_CHECKING, Protocol, runtime_checkable

if TYPE_CHECKING:
    from techai_webutils.core.domain import ParsedDocument


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
