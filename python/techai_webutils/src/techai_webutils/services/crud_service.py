"""Base CRUD service wrapping a Repository.

Mirrors Go's ``services/service/service.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, TypeVar

from techai_webutils.core.interfaces.crud_service import CrudService

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import Page, PageRequest
    from techai_webutils.core.interfaces.repository import Repository

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class BaseCrudService[T, P, ID](CrudService[T, P, ID]):
    """Service that delegates all CRUD operations to a Repository.

    Subclasses can override individual methods to add business rules,
    authorization, event publishing, or caching.
    """

    def __init__(self, repo: Repository[T, P, ID]) -> None:
        """Bind the service to the ``repo`` all CRUD operations delegate to."""
        self._repo = repo

    async def get(self, entity_id: ID) -> T:
        """Delegate to repository.get."""
        return await self._repo.get(entity_id)

    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Delegate to repository.list."""
        return await self._repo.list(params, page)

    async def create(self, entity: T) -> T:
        """Delegate to repository.create."""
        return await self._repo.create(entity)

    async def update(self, entity_id: ID, entity: T) -> T:
        """Delegate to repository.update."""
        return await self._repo.update(entity_id, entity)

    async def delete(self, entity_id: ID) -> None:
        """Delegate to repository.delete."""
        await self._repo.delete(entity_id)
