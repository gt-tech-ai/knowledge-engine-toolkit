"""Repositories layer - data access abstractions.

Provides the repository tier (``tier``, ``RepositoryBase``: decorated CRUD over a Store plus
custom queries), ``DelegatingRepository`` for a consumer repository, ``CursorCodec`` for
pagination, and error mapping.

Repos implements the fourth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from techai_webutils.repos.cursor import CursorCodec, CursorPayload
from techai_webutils.repos.errors.mapper import map_db_error
from techai_webutils.repos.repository import DelegatingRepository, RepositoryBase, tier

__all__ = [
    "CursorCodec",
    "CursorPayload",
    "DelegatingRepository",
    "RepositoryBase",
    "map_db_error",
    "tier",
]
