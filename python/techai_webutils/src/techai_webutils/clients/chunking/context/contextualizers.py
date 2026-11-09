"""The ``none``, ``header`` and ``llm`` chunk contextualizers.

``header`` is deterministic and free: ``"{document} > {section} > {subsection}"``, then a
table chunk's caption on the next line, then a blank line and the chunk. ``llm`` asks the
injected model for one or two sentences situating the chunk in the document (the
document's first ``max_document_chars`` characters), at temperature 0, at most
``max_context_tokens`` tokens, with at most ``concurrency`` calls in flight; a model error
propagates (coded by the provider).
"""

from __future__ import annotations

import asyncio
from typing import TYPE_CHECKING, override

from techai_webutils.core.interfaces.chunk_contextualizer import ChunkContextualizer
from techai_webutils.core.interfaces.llm import LLMConfig, LLMMessage

if TYPE_CHECKING:
    from collections.abc import Sequence

    from techai_webutils.core.interfaces.chunk_contextualizer import ContextDocument
    from techai_webutils.core.interfaces.chunker import StructuredChunk
    from techai_webutils.core.interfaces.llm import LLMProvider

_SEPARATOR = " > "
_INSTRUCTIONS = (
    "You situate a chunk of a document so it can be found by search. Reply with one "
    "or two "
    "sentences naming what the chunk is about within the document (its subject, "
    "period and "
    "section). Reply with the context only."
)


class NoContextualizer(ChunkContextualizer):
    """Returns each chunk's text unchanged (the default)."""

    @override
    async def contextualize(
        self, document: ContextDocument, chunks: Sequence[StructuredChunk]
    ) -> list[str]:
        """Return the chunk texts."""
        return [chunk.text for chunk in chunks]


class HeaderContextualizer(ChunkContextualizer):
    """Prepends the document name, section path and table caption."""

    @override
    async def contextualize(
        self, document: ContextDocument, chunks: Sequence[StructuredChunk]
    ) -> list[str]:
        """Return each chunk's text after its header lines.

        (none when there is nothing to say).
        """
        return [_with_header(document.name, chunk) for chunk in chunks]


class LlmContextualizer(ChunkContextualizer):
    """Prepends a model-written context, one model call per chunk."""

    def __init__(
        self,
        llm: LLMProvider,
        *,
        max_document_chars: int,
        max_context_tokens: int,
        concurrency: int,
    ) -> None:
        """Ask ``llm`` with at most ``max_document_chars`` of the document per call."""
        self._llm = llm
        self._max_document_chars = max_document_chars
        self._config = LLMConfig(temperature=0.0, max_tokens=max_context_tokens)
        self._slots = asyncio.Semaphore(concurrency)

    @override
    async def contextualize(
        self, document: ContextDocument, chunks: Sequence[StructuredChunk]
    ) -> list[str]:
        """Return each chunk's text after the model's context for it."""
        excerpt = document.text[: self._max_document_chars]
        return list(
            await asyncio.gather(*(self._one(document, excerpt, c) for c in chunks))
        )

    async def _one(
        self, document: ContextDocument, excerpt: str, chunk: StructuredChunk
    ) -> str:
        """Ask the model for one chunk's context."""
        prompt = (
            f"<document name={document.name!r}>\n{excerpt}\n</document>\n"
            f"<chunk>\n{chunk.text}\n</chunk>"
        )
        async with self._slots:
            response = await self._llm.complete(
                [LLMMessage("system", _INSTRUCTIONS), LLMMessage("user", prompt)],
                self._config,
            )
        context = response.content.strip()
        return f"{context}\n\n{chunk.text}" if context else chunk.text


def _with_header(name: str, chunk: StructuredChunk) -> str:
    """Return ``chunk.text`` after its ``name > section`` line and caption.

    When there are any.
    """
    lines = [_SEPARATOR.join(part for part in (name, *chunk.section_path) if part)]
    if chunk.caption:
        lines.append(chunk.caption)
    header = "\n".join(line for line in lines if line)
    return f"{header}\n\n{chunk.text}" if header else chunk.text
