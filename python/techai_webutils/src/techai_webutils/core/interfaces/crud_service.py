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
        """Retrieve an entity by ID with authorization checks."""
        ...

    @abstractmethod
    async def list(self, params: P, page: PageRequest) -> Page[T]:
        """Retrieve entities matching parameters with authorization filtering."""
        ...

    @abstractmethod
    async def create(self, entity: T) -> T:
        """Validate and create a new entity, publishing domain events."""
        ...

    @abstractmethod
    async def update(self, entity_id: ID, entity: T) -> T:
        """Validate and update an entity, publishing domain events."""
        ...

    @abstractmethod
    async def delete(self, entity_id: ID) -> None:
        """Remove an entity with authorization checks, publishing domain events."""
        ...


class ServiceHooks[T]:
    """Lifecycle hooks for service operations.

    All methods are no-ops by default. Subclasses override only
    the hooks they need.
    """

    async def before_create(self, entity: T) -> None:
        """Run pre-creation logic for the given entity."""

    async def after_create(self, entity: T) -> None:
        """Run post-creation logic for the given entity."""

    async def before_update(self, entity: T) -> None:
        """Run pre-update logic for the given entity."""

    async def after_update(self, entity: T) -> None:
        """Run post-update logic for the given entity."""

    async def before_delete(self, entity_id: object) -> None:
        """Run pre-deletion logic for the given entity ID."""

    async def after_delete(self, entity_id: object) -> None:
        """Run post-deletion logic for the given entity ID."""
