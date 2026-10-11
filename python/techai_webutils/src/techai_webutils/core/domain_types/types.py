"""Shared domain types used across the library.

Mirrors the Go types in go/core/types/types.go. Every type here must
stay in sync with its Go counterpart to ensure consistent serialization
and cross-service communication.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import UTC, datetime
from enum import StrEnum
from typing import TypeVar

T = TypeVar("T")


class ID(str):
    """Typed identifier for domain entities."""

    __slots__ = ()

    def is_empty(self) -> bool:
        """Return True if the ID is empty."""
        return not self


@dataclass(frozen=True)
class Option[T]:
    """Optional value container.

    Use ``Some(value)`` to create an Option with a value,
    ``none()`` to create an empty Option.
    """

    _value: T | None = field(default=None, repr=False)
    """Wrapped value when present, None when the Option is empty."""
    _valid: bool = field(default=False)
    """True when the Option holds a value, False when it is empty."""

    def get(self) -> tuple[T | None, bool]:
        """Return the value and whether it exists."""
        return self._value, self._valid

    def or_else(self, fallback: T) -> T:
        """Return the value if present, otherwise the fallback."""
        if self._valid and self._value is not None:
            return self._value
        return fallback

    @property
    def valid(self) -> bool:
        """Whether the Option contains a value."""
        return self._valid


def some[T](value: T) -> Option[T]:
    """Create an Option containing a value."""
    return Option(_value=value, _valid=True)


def none() -> Option[T]:
    """Create an empty Option."""
    return Option(_value=None, _valid=False)


@dataclass
class Page[T]:
    """Paginated result set."""

    items: list[T] = field(default_factory=list)
    """The page's slice of result items."""
    total: int = 0
    """Total number of items across all pages."""
    page_size: int = 0
    """Maximum number of items per page."""
    page_number: int = 0
    """1-based index of this page within the result set."""
    next_cursor: str = ""
    """Opaque cursor locating the next page, empty when there is none."""

    def has_more(self) -> bool:
        """Return True if there are more pages available."""
        return (self.page_number * self.page_size) < self.total


@dataclass
class PageRequest:
    """Pagination parameters for list queries."""

    page_size: int = 20
    """Requested maximum number of items per page."""
    page_number: int = 1
    """Requested 1-based page index."""
    cursor: str = ""
    """Opaque cursor requesting the page after a prior result, empty for the first page."""


class SortOrder(StrEnum):
    """Sort direction."""

    ASC = "asc"
    """Ascending sort direction."""
    DESC = "desc"
    """Descending sort direction."""


@dataclass(frozen=True)
class SortField:
    """Field to sort by with direction."""

    field: str
    """Name of the field to sort by."""
    order: SortOrder = SortOrder.ASC
    """Sort direction applied to the field."""


@dataclass
class Timestamps:
    """Common timestamp fields for entities."""

    created_at: datetime = field(default_factory=lambda: datetime.now(tz=UTC))
    """UTC time the entity was created."""
    updated_at: datetime = field(default_factory=lambda: datetime.now(tz=UTC))
    """UTC time the entity was last modified."""
    deleted_at: datetime | None = None
    """UTC time the entity was soft-deleted, None while active."""

    def soft_delete(self) -> None:
        """Mark the entity as deleted."""
        self.deleted_at = datetime.now(tz=UTC)

    @property
    def is_deleted(self) -> bool:
        """Whether the entity has been soft-deleted."""
        return self.deleted_at is not None
