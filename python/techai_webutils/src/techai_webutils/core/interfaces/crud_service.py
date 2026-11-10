"""CRUD service interface for business logic layer.

Mirrors Go's ``interfaces.Service[T, P, ID]`` and ``ServiceHooks[T]``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING, TypeVar

if TYPE_CHECKING:
    from techai_webutils.core.domain_types.types import Page, PageRequest

T = TypeVar("T")
P = TypeVar("P")
ID = TypeVar("ID")


class CrudService[T, P, ID](ABC):
    """Generic business logic layer interface.

    Wraps a Repository with business rules, authorization, validation,
    event publishing, and caching.
    """

    @abstractmethod
    async def get(self, entity_id: ID) -> T:
        """Retrieve an entity by ID with authorization checks.

        Args:
            entity_id: The entity's identifier.

        Returns:
            The entity, when the caller may read it.

        """
        ...

    @abstractmethod
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Retrieve entities matching parameters with authorization filtering.

        Args:
            params: The filter the entities must match.
            page: Which page of results to return.

        Returns:
            One page of the entities the caller may read.

        """
        ...

    @abstractmethod
    async def create(self, entity: T) -> T:
        """Validate and create a new entity, publishing domain events.

        Args:
            entity: The entity to create.

        Returns:
            The created entity, as stored.

        """
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Validate and update an entity, publishing domain events.

        Args:
            entity_id: The entity's identifier.
            entity: The entity's new state.

        Returns:
            The updated entity, as stored.

        """
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity with authorization checks, publishing domain events.

        Args:
            entity_id: The entity's identifier.

        """
        ...


class ServiceHooks[T]:
    """Lifecycle hooks for service operations.

    All methods are no-ops by default. Subclasses override only
    the hooks they need.
    """

    async def before_create(self, _entity: T, /) -> None:
        """Run pre-creation logic for the given entity.

        Args:
            _entity: The entity about to be created.

        """

    async def after_create(self, _entity: T, /) -> None:
        """Run post-creation logic for the given entity.

        Args:
            _entity: The entity just created.

        """

    async def before_update(self, _entity: T, /) -> None:
        """Run pre-update logic for the given entity.

        Args:
            _entity: The entity's new state, about to be stored.

        """

    async def after_update(self, _entity: T, /) -> None:
        """Run post-update logic for the given entity.

        Args:
            _entity: The entity's new state, just stored.

        """

    async def before_delete(self, _entity_id: object, /) -> None:
        """Run pre-deletion logic for the given entity ID.

        Args:
            _entity_id: The identifier of the entity about to be deleted.

        """

    async def after_delete(self, _entity_id: object, /) -> None:
        """Run post-deletion logic for the given entity ID.

        Args:
            _entity_id: The identifier of the entity just deleted.

        """
