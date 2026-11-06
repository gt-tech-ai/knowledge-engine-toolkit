"""Runs a sink's writes, deletes and metadata updates through the client tier.

``sink_from_config`` returns an undecorated ``Sink``; ``decorate`` gives it a ``ClientBase``, so
each call carries the timeout, retry, breaker and observability the consumer's
``decoration.clients`` configures for the sink's name. Every sink call is idempotent (chunk ids are
derived from the document, so a re-sent write overwrites the same chunks; deletes and metadata sets
repeat safely), so each may be retried when the config turns retry on.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, override

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.interfaces.sink import Sink
from techai_webutils.foundation.lifecycle import DelegatingAsyncResource

if TYPE_CHECKING:
    from collections.abc import Mapping, Sequence

    from techai_webutils.core.interfaces.sink import ChunkPayload, SinkDocument
    from techai_webutils.foundation.decorate import Decorations


def decorate(sink: Sink, decorations: Decorations, name: str) -> Sink:
    """Return ``sink`` with every call run through the client tier as the client ``name``.

    Args:
        sink: The sink to wrap.
        decorations: The decorators the client tier wraps each call in.
        name: The client name (its ``decoration.clients`` key).

    Returns:
        The decorated sink; entering it enters ``sink``.

    """
    return _DecoratedSink(sink, ClientBase(Sink, decorations, name))


class _DecoratedSink(DelegatingAsyncResource[Sink], Sink):
    """A sink whose calls run through a ClientBase; its lifecycle is the wrapped one's."""

    def __init__(self, inner: Sink, client: ClientBase) -> None:
        """Bind the wrapped sink and the client tier it runs through.

        Args:
            inner: The wrapped sink.
            client: The client tier it runs through.

        """
        self._inner = inner
        self._client = client

    @override
    async def write(
        self,
        document: SinkDocument,
        chunks: Sequence[ChunkPayload],
        vectors: Sequence[Sequence[float]] | None,
    ) -> None:
        """Write the document's chunks through the tier.

        Args:
            document: The document the chunks belong to.
            chunks: The chunks.
            vectors: One vector per chunk, or None when the sink embeds by itself.

        """
        await self._client.exec(
            "write", lambda: self._inner.write(document, chunks, vectors)
        )

    @override
    async def delete(self, org_id: str, document_id: str) -> None:
        """Delete the document's chunks through the tier.

        Args:
            org_id: The document's org.
            document_id: The document.

        """
        await self._client.exec("delete", lambda: self._inner.delete(org_id, document_id))

    @override
    async def update_metadata(
        self, org_id: str, document_id: str, fields: Mapping[str, str]
    ) -> None:
        """Set metadata on the document's chunks through the tier.

        Args:
            org_id: The document's org.
            document_id: The document.
            fields: The fields to set.

        """
        await self._client.exec(
            "update_metadata",
            lambda: self._inner.update_metadata(org_id, document_id, fields),
        )
