"""Base service interface for lifecycle management.

Provides the abstract contract for service entry points with standard
startup, shutdown, and health check lifecycle hooks.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import Any


class BaseService(ABC):
    """Base class for service implementations.

    Provides standard lifecycle hooks and health check interface.
    A consumer's Python service entry points should inherit from this class.
    """

    def __init__(self, name: str) -> None:
        """Initialize the service with a name and an unhealthy starting state."""
        self._name = name
        self._healthy = False

    @property
    def name(self) -> str:
        """The service name."""
        return self._name

    @property
    def is_healthy(self) -> bool:
        """Whether the service is healthy."""
        return self._healthy

    @abstractmethod
    async def startup(self) -> None:
        """Initialize service dependencies. Called once at startup."""
        ...

    @abstractmethod
    async def shutdown(self) -> None:
        """Clean up resources. Called once at shutdown."""
        ...

    async def health_check(self) -> dict[str, Any]:
        """Return health status.

        Returns:
            Dict with "status" (healthy/unhealthy) and optional details.

        """
        return {
            "service": self._name,
            "status": "healthy" if self._healthy else "unhealthy",
        }
