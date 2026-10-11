"""Repositories layer - data access abstractions.

Provides BaseRepository (wrapping Store), CursorCodec for pagination,
RepositoryBuilder for decorator composition, and error mapping.

Repos implements the fourth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from techai_webutils.repos.cursor import CursorCodec, CursorPayload
from techai_webutils.repos.decorators.builder import RepositoryBuilder
from techai_webutils.repos.errors.mapper import map_db_error
from techai_webutils.repos.repository import BaseRepository

__all__ = [
    "BaseRepository",
    "CursorCodec",
    "CursorPayload",
    "RepositoryBuilder",
    "map_db_error",
]
