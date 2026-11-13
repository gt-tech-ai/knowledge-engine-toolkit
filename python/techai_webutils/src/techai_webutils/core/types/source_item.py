"""``SourceItem``: where one document's body lives, for a ``SourceReader``.

One value type carries every reader kind's address, so a batch of mixed items can be
routed by the caller to the reader its connector kind selects. A reader reads only its own
fields: ``s3`` reads ``bucket`` + ``key``; ``github`` reads ``owner``, ``repo``,
``commit``, ``path`` and ``installation_id``. Every field defaults, so an item serialized
before a field existed still loads.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class SourceItem:
    """The address of one document body in its source system."""

    bucket: str = ""
    """The object-store bucket (``s3``)."""
    key: str = ""
    """The object key in ``bucket`` (``s3``)."""
    owner: str = ""
    """The repository owner, a user or organization login (``github``)."""
    repo: str = ""
    """The repository name (``github``)."""
    commit: str = ""
    """The commit SHA the file is read at, so every read of one sync sees one tree
    (``github``)."""
    path: str = ""
    """The file's path from the repository root (``github``)."""
    installation_id: int = 0
    """The GitHub App installation whose token reads the repository (``github``)."""
