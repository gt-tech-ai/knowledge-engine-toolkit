"""Repository interface for generic data access.

Mirrors Go's ``interfaces.Repository[T, P, ID]``. All service-layer code
depends on this contract; concrete implementations live in the repos layer.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING, TypeVar

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable

    from techai_webutils.core.domain_types.types import Page, PageRequest

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")
R = TypeVar("R")


class Repository[T, P, ID](ABC):
    """Generic data access interface.

    T = entity type, P = query/filter params, ID = identifier type.
    """

    @abstractmethod
    async def get(self, entity_id: ID) -> T:
        """Retrieve an entity by ID. Raises NotFoundError if not found."""
        ...

    @abstractmethod
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Retrieve entities matching parameters with pagination."""
        ...

    @abstractmethod
    async def create(self, entity: T) -> T:
        """Create a new entity and return it with generated fields populated."""
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an existing entity. Raises NotFoundError if not found."""
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity by ID. Raises NotFoundError if not found."""
        ...

    @abstractmethod
    async def exists(self, entity_id: ID) -> bool:
        """Check if an entity with the given ID exists."""
        ...


class Transaction(ABC):
    """Database transaction handle."""

    @abstractmethod
    async def commit(self) -> None:
        """Commit the transaction."""
        ...

    @abstractmethod
    async def rollback(self) -> None:
        """Abort the transaction."""
        ...


class TransactionManager(ABC):
    """Handles transaction lifecycle."""

    @abstractmethod
    async def begin(self) -> Transaction:
        """Start a new transaction."""
        ...

    @abstractmethod
    async def with_transaction(self, fn: Callable[[], Awaitable[R]]) -> R:
        """Execute fn within a transaction, auto-committing or rolling back."""
        ...
