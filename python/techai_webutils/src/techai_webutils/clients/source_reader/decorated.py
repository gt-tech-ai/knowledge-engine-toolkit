"""Runs a source reader's reads through the client tier.

``source_reader_from_config`` returns an undecorated ``SourceReader``; ``decorate`` gives
it a ``ClientBase``, so each read carries the timeout, retry, breaker and observability
the consumer's ``decoration.clients`` configures for the reader's name. A read has no side
effects, so it may be retried when the config turns retry on.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.clients.client import ClientBase
from techai_webutils.core.interfaces.source_reader import SourceReader

if TYPE_CHECKING:
    from techai_webutils.core.types.source_item import SourceItem
    from techai_webutils.foundation.decorate import Decorations


def decorate(reader: SourceReader, decorations: Decorations, name: str) -> SourceReader:
    """Return ``reader`` with every read run through the client tier.

    Args:
        reader: The reader to wrap.
        decorations: The decorators the client tier wraps each read in.
        name: The client name (its ``decoration.clients`` key).

    Returns:
        ``reader`` with every read run through the client tier.

    """
    return _DecoratedReader(reader, ClientBase(SourceReader, decorations, name))


@final
class _DecoratedReader(SourceReader):
    """A source reader whose reads run through a ``ClientBase``."""

    def __init__(self, inner: SourceReader, client: ClientBase) -> None:
        """Bind the wrapped reader and the client tier it runs through.

        Args:
            inner: The wrapped reader.
            client: The client tier it runs through.

        """
        self._inner = inner
        self._client = client

    @override
    async def read(self, item: SourceItem) -> bytes:
        """Read ``item`` through the tier as op ``read``.

        Args:
            item: The document to read.

        Returns:
            The wrapped reader's bytes.

        """
        return await self._client.exec("read", lambda: self._inner.read(item))
