"""Repository interface for generic data access.

Mirrors Go's ``interfaces.Repository[T, P, ID]``. All service-layer code
depends on this contract; concrete implementations live in the repos layer.
"""

from __future__ import annotations

import contextvars
from abc import ABC, abstractmethod
from contextlib import contextmanager
from typing import TYPE_CHECKING, TypeVar

if TYPE_CHECKING:
    from collections.abc import Awaitable, Callable, Generator

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
        """Retrieve an entity by ID. Raises NotFoundError if not found.

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
        """Create a new entity and return it with generated fields populated.

        Args:
            entity: The entity to store.

        Returns:
            The stored entity, with generated fields (ids, timestamps) filled.

        """
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Update an existing entity. Raises NotFoundError if not found.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The stored entity after the update.

        """
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity by ID. Raises NotFoundError if not found.

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
            ``True`` when an entity with ``entity_id`` is stored.

        """
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
        """Start a new transaction.

        Returns:
            The open transaction; the caller commits or rolls it back.

        """
        ...

    @abstractmethod
    async def with_transaction(self, fn: Callable[[], Awaitable[R]]) -> R:
        """Execute fn within a transaction, auto-committing or rolling back.

        Args:
            fn: The work to run inside the transaction.

        Returns:
            What ``fn`` returns, after the transaction commits.

        """
        ...


_in_transaction: contextvars.ContextVar[bool] = contextvars.ContextVar(
    "techai_webutils_in_transaction", default=False
)


def in_transaction() -> bool:
    """Report whether the current task runs inside a workflow's transaction.

    (Go ``InTx``).

    Returns:
        Whether a transaction is active.

    """
    return _in_transaction.get()


@contextmanager
def marking_transaction() -> Generator[None]:
    """Mark the current task.

    As inside a transaction until the block exits (Go ``WithInTx``).
    """
    token = _in_transaction.set(True)
    try:
        yield
    finally:
        _in_transaction.reset(token)
