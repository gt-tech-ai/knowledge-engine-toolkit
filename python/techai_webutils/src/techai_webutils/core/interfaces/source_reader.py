"""``SourceReader``: reads one document's body from the system it lives in.

A connector lists documents; a reader fetches each one's bytes. The caller picks the
reader by the item's connector kind, so an object in a bucket and a file in a repository
flow through the same ingestion path.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from techai_webutils.core.types.source_item import SourceItem


class SourceReader(ABC):
    """Reads a document body addressed by a ``SourceItem``."""

    @abstractmethod
    async def read(self, item: SourceItem) -> bytes:
        """Return the body ``item`` addresses.

        Args:
            item: Where the body lives; the reader reads only its own kind's fields.

        Returns:
            The body's bytes.

        """
        ...
