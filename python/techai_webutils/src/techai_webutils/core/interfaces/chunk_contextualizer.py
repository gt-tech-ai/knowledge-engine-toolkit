"""``ChunkContextualizer``: situate each chunk in its document before it is embedded.

A chunk cut from the middle of a document loses what it is about ("revenue grew 3%": whose, which
quarter?). A contextualizer returns, per chunk, the text to embed and to index for keyword search:
the chunk's text with a short context first. The source span is unchanged; only the indexed text
gains the context. Kinds (``clients.chunking.context``): ``none`` (the default) returns the
chunk text unchanged, ``header`` prepends the document name, section path and table caption, and
``llm`` prepends a model-written context (one model call per chunk: a priced dial the consumer
turns on only with a budget).
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.chunker import StructuredChunk


@dataclass(frozen=True, slots=True)
class ContextDocument:
    """The document the chunks came from."""

    name: str
    """The document's display name (a title or file name)."""
    text: str
    """The document's full text (the ``llm`` kind sends a bounded prefix of it)."""


class ChunkContextualizer(ABC):
    """Gives each chunk its document context."""

    @abstractmethod
    async def contextualize(
        self, document: ContextDocument, chunks: Sequence[StructuredChunk]
    ) -> list[str]:
        """Return the text to embed and index for each of ``chunks``, in order."""
