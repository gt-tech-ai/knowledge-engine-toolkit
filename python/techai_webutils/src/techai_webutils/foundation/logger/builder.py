"""Logger builder with kind enum, config dataclass, and factory function.

Creates logger instances based on configuration. Supports STRUCTLOG and STDLIB
logger kinds.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum
from typing import IO, TYPE_CHECKING

from techai_webutils.core.errors import AppValueError
from techai_webutils.foundation.logger.logger import new_logger
from techai_webutils.foundation.logger.stdlib_logger import StdlibLogger

if TYPE_CHECKING:
    from techai_webutils.core.interfaces.logger import Logger


class LoggerKind(StrEnum):
    """Available logger implementations."""

    STRUCTLOG = "structlog"
    """structlog-backed JSON logger (the default production logger)."""
    STDLIB = "stdlib"
    """stdlib ``logging`` logger (fallback where structlog is undesirable)."""


@dataclass
class LoggerConfig:
    """Configuration for logger creation.

    Attributes:
        kind: Which logger implementation to use.
        level: Log level (DEBUG, INFO, WARNING, ERROR).
        stream: Output stream. Defaults to sys.stdout.

    """

    kind: LoggerKind = LoggerKind.STRUCTLOG
    """Which logger implementation the factory builds (STRUCTLOG or STDLIB)."""
    level: str = "INFO"
    """Minimum level emitted (DEBUG, INFO, WARNING, ERROR, CRITICAL)."""
    stream: IO[str] | None = None
    """Output stream for log lines; defaults to ``sys.stdout`` when None."""


def default_config() -> LoggerConfig:
    """Return a default logger config (STRUCTLOG with INFO level)."""
    return LoggerConfig(kind=LoggerKind.STRUCTLOG, level="INFO")


def new_logger_from_config(config: LoggerConfig) -> Logger:
    """Create a logger instance from config.

    Args:
        config: Logger configuration specifying kind and parameters.

    Returns:
        A Logger implementation matching the requested kind.

    Raises:
        ValueError: If the kind is unknown.

    """
    if config.kind == LoggerKind.STRUCTLOG:
        return new_logger(level=config.level, stream=config.stream)

    if config.kind == LoggerKind.STDLIB:
        return StdlibLogger(level=config.level, stream=config.stream)

    msg = f"unknown logger kind: {config.kind}"
    raise AppValueError(msg)
