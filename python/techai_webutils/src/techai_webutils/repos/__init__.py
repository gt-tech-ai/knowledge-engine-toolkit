"""Repositories layer - data access abstractions.

Provides BaseRepository (wrapping Store), DelegatingRepository (CRUD from a decorated
Repository), CursorCodec for pagination, RepositoryBuilder for decorator composition,
repo_op_chain for custom ops, and error mapping.

Repos implements the fourth tier (ARCHITECTURE.md#layers-and-import-direction):
core -> foundation -> clients -> repos -> services -> pipelines -> workflows -> controllers
"""

from techai_webutils.repos.cursor import CursorCodec, CursorPayload
from techai_webutils.repos.decorators.builder import RepositoryBuilder
from techai_webutils.repos.errors.mapper import map_db_error
from techai_webutils.repos.op_chain import RepoOpDeps, repo_op_chain
from techai_webutils.repos.repository import BaseRepository, DelegatingRepository

__all__ = [
    "BaseRepository",
    "CursorCodec",
    "CursorPayload",
    "DelegatingRepository",
    "RepoOpDeps",
    "RepositoryBuilder",
    "map_db_error",
    "repo_op_chain",
]
