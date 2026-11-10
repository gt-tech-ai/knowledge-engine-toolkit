"""Store interface for infrastructure-level data access.

Mirrors Go's ``interfaces.Store[T, P, ID]``. Same CRUD surface as
Repository but represents the infrastructure boundary (e.g., asyncpg,
SQLAlchemy) rather than the business-logic boundary.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING, TypeVar

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import Page, PageRequest

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class Store[T, P, ID](ABC):
    """Low-level data access interface.

    T = entity type, P = query/filter params, ID = identifier type.
    """

    @abstractmethod
    async def get(self, entity_id: ID) -> T:
        """Retrieve an entity by ID.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The stored entity.

        """
        ...

    @abstractmethod
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Retrieve entities matching parameters with pagination.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            One page of matching entities.

        """
        ...

    @abstractmethod
    async def create(self, entity: T) -> T:
        """Persist a new entity and return it with generated fields populated.

        Args:
            entity: The entity to persist.

        Returns:
            The persisted entity, with generated fields filled.

        """
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Modify an existing entity identified by id.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The persisted entity after the update.

        """
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity by ID.

        Args:
            entity_id: The entity's identifier.

        """
        ...

    @abstractmethod
    async def exists(self, entity_id: ID) -> bool:
        """Check if an entity with the given ID exists.

        Args:
            entity_id: The entity's identifier.

        Returns:
            ``True`` when an entity with ``entity_id`` is persisted.

        """
        ...
