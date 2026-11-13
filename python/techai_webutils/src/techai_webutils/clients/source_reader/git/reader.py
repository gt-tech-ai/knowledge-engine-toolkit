"""``GitSourceReader``: reads an item's ``path`` from a mounted git work tree."""

from __future__ import annotations

import asyncio
from pathlib import PurePosixPath
from typing import TYPE_CHECKING, final, override

from techai_webutils.core.errors import InternalError, InvalidInputError, NotFoundError
from techai_webutils.core.interfaces.source_reader import SourceReader

if TYPE_CHECKING:
    from pathlib import Path

    from techai_webutils.core.types.source_item import SourceItem


@final
class GitSourceReader(SourceReader):
    """Reads each item's ``path`` under one work tree root.

    The path is resolved (``..`` segments and symlinks followed) before it is read, and a
    path that is empty, absolute, or resolves outside the root is refused, so an item
    from a queue can never read a file beside the work tree. The blocking read runs in a
    worker thread.
    """

    def __init__(self, root: Path) -> None:
        """Read under ``root``.

        Args:
            root: The work tree's directory; the platform sets it, never a tenant.

        """
        self._root = root

    @override
    async def read(self, item: SourceItem) -> bytes:
        """Return the bytes of ``item.path`` under the root.

        A path that is empty, absolute or resolves outside the root is
        ``INVALID_INPUT``; a missing file is ``NOT_FOUND``; a file that exists but
        cannot be read is ``INTERNAL``.

        Args:
            item: The file's slash-separated path from the work tree root.

        Returns:
            The file's bytes.

        """
        return await asyncio.to_thread(self._read, item.path)

    def _read(self, path: str) -> bytes:
        """Resolve ``path`` inside the root and read it (blocking).

        Args:
            path: The file's slash-separated path from the work tree root.

        Returns:
            The file's bytes.

        Raises:
            InvalidInputError: The path is empty, absolute, or resolves outside the root.
            NotFoundError: No file is at the path.
            InternalError: The file exists but cannot be read.

        """
        if not path or PurePosixPath(path).is_absolute():
            msg = "git reader: the path must be relative to the work tree"
            raise InvalidInputError(msg, path=path)
        root = self._root.resolve()
        target = (root / path).resolve()
        if not target.is_relative_to(root):
            msg = "git reader: the path leaves the work tree"
            raise InvalidInputError(msg, path=path)
        try:
            return target.read_bytes()
        except FileNotFoundError as err:
            msg = "git reader: no file at the path"
            raise NotFoundError(msg, path=path) from err
        except OSError as err:
            msg = "git reader: the file cannot be read"
            raise InternalError(msg) from err
