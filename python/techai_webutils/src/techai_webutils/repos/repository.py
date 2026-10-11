"""Base repository implementation wrapping a Store.

Mirrors Go's ``repos/repository/repository.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, TypeVar

from techai_webutils.core.interfaces.repository import Repository

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.store import Store

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class BaseRepository[T, P, ID](Repository[T, P, ID]):
    """Repository that delegates all CRUD operations to a Store.

    Subclasses can override individual methods to add caching, validation,
    or other repository-level concerns.
    """

    def __init__(self, store: Store[T, P, ID]) -> None:
        """Bind the repository to the backing ``store`` it delegates to."""
        self._store = store

    async def get(self, entity_id: ID) -> T:
        """Delegate to store.get."""
        return await self._store.get(entity_id)

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to store.list."""
        return await self._store.list(params, page)

    async def create(self, entity: T) -> T:
        """Delegate to store.create."""
        return await self._store.create(entity)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to store.update."""
        return await self._store.update(entity_id, entity)

    async def delete(self, entity_id: ID) -> None:
        """Delegate to store.delete."""
        await self._store.delete(entity_id)

    async def exists(self, entity_id: ID) -> bool:
        """Delegate to store.exists."""
        return await self._store.exists(entity_id)
