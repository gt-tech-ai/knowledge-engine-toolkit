"""Circuit breaker interface.

Mirrors Go's ``interfaces.CircuitBreaker``. The concrete implementation
lives in ``foundation.resilience.circuit_breaker``.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from typing import TYPE_CHECKING, Self

if TYPE_CHECKING:
    import types
    from collections.abc import Callable


class CircuitBreakerInterface(ABC):
    """Prevents cascading failures by failing fast when downstream is unhealthy."""

    @abstractmethod
    def execute(self, fn: Callable[[], object]) -> object:
        """Run fn through the circuit breaker.

        Raises an error if the circuit is open or fn fails.
        """
        ...

    @abstractmethod
    def __enter__(self) -> Self:
        """Enter the circuit breaker context."""
        ...

    @abstractmethod
    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: types.TracebackType | None,
    ) -> None:
        """Exit the circuit breaker context."""
        ...
