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
        """Retrieve an entity by ID."""
        ...

    @abstractmethod
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Retrieve entities matching parameters with pagination."""
        ...

    @abstractmethod
    async def create(self, entity: T) -> T:
        """Persist a new entity and return it with generated fields populated."""
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Modify an existing entity identified by id."""
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity by ID."""
        ...

    @abstractmethod
    async def exists(self, entity_id: ID) -> bool:
        """Check if an entity with the given ID exists."""
        ...
