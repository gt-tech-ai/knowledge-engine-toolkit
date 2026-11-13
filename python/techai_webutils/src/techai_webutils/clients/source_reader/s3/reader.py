"""``S3SourceReader``: reads an item's ``bucket`` + ``key`` through a storage client."""

from __future__ import annotations

from typing import TYPE_CHECKING, final, override

from techai_webutils.core.interfaces.source_reader import SourceReader

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.storage import StorageClient
    from techai_webutils.core.types.source_item import SourceItem


@final
class S3SourceReader(SourceReader):
    """Downloads each item from the object store the injected client reaches."""

    def __init__(self, storage: StorageClient) -> None:
        """Read through ``storage``.

        Args:
            storage: The object-store client; its own client tier carries the timeout,
                retry and observability of each download.

        """
        self._storage = storage

    @override
    async def read(self, item: SourceItem) -> bytes:
        """Download ``item.key`` from ``item.bucket``.

        Args:
            item: The object's bucket and key.

        Returns:
            The object's bytes.

        """
        return await self._storage.download(item.bucket, item.key)
