"""Configuration loader interface.

Mirrors Go's ``interfaces.ConfigLoader``. The interface deliberately omits
a ``load()`` method; loading is handled by the concrete type's constructor.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TypeVar

T = TypeVar("T")


class ConfigLoader(ABC):
    """Read-only access to loaded configuration values."""

    @abstractmethod
    def get(self, key: str) -> object | None:
        """Return the raw value for a key, or None if not set."""
        ...

    @abstractmethod
    def get_string(self, key: str) -> str:
        """Return the string value for a key."""
        ...

    @abstractmethod
    def get_int(self, key: str) -> int:
        """Return the int value for a key."""
        ...

    @abstractmethod
    def get_bool(self, key: str) -> bool:
        """Return the bool value for a key."""
        ...

    @abstractmethod
    def unmarshal(self, target_type: type[T]) -> T:
        """Decode the full configuration into the target type."""
        ...
