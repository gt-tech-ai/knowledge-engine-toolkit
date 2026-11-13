"""``StubSourceReader``: the zero-infrastructure default reader."""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.source_reader import SourceReader

if TYPE_CHECKING:
    from techai_webutils.core.types.source_item import SourceItem


@final
class StubSourceReader(SourceReader):
    """Reads every item as an empty body, so a graph boots with no storage or network."""

    @override
    async def read(self, item: SourceItem) -> bytes:
        """Return an empty body.

        Args:
            item: Ignored.

        Returns:
            ``b""``.

        """
        return b""
