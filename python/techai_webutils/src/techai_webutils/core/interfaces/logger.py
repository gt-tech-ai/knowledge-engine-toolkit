"""Logger interface for structured logging.

Defines the abstract contract for logger implementations.
Foundation implementations satisfy this interface; consumers depend only on it.
"""

from __future__ import annotations

from abc import ABC, abstractmethod


class Logger(ABC):
    """Abstract structured logger with level-based logging.

    Implementations: structlog JSON (default), stdlib logging.
    All service code depends on this interface, never on a concrete logger.
    """

    @abstractmethod
    def debug(self, msg: str, **kwargs: object) -> None:
        """Log at DEBUG level with optional key-value context."""
        ...

    @abstractmethod
    def info(self, msg: str, **kwargs: object) -> None:
        """Log at INFO level with optional key-value context."""
        ...

    @abstractmethod
    def warning(self, msg: str, **kwargs: object) -> None:
        """Log at WARNING level with optional key-value context."""
        ...

    @abstractmethod
    def error(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level with optional key-value context."""
        ...

    @abstractmethod
    def exception(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level with exception info attached."""
        ...

    @abstractmethod
    def bind(self, **kwargs: object) -> Logger:
        """Return a new Logger with the given key-value pairs pre-bound."""
        ...
