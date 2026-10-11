"""Stdlib logging implementation with JSON output.

Uses Python's built-in logging module with a JSON formatter. No external
dependencies (no structlog). Suitable for lightweight services or environments
where structlog is not available.
"""

from __future__ import annotations

import json
import logging
import sys
from typing import IO, Any, TextIO, cast

from techai_webutils.core.interfaces.logger import Logger


class _JSONFormatter(logging.Formatter):
    """Formats log records as single-line JSON objects."""

    def format(self, record: logging.LogRecord) -> str:
        """Render a log record as a single-line JSON string.

        Emits the canonical cross-service fields (message, level, logger,
        timestamp) and merges any bound context stashed on the record's
        ``_extra`` attribute. Non-serialisable values fall back to ``str``.
        """
        entry: dict[str, Any] = {
            "message": record.getMessage(),
            "level": record.levelname,
            "logger": record.name,
            "timestamp": self.formatTime(record, self.datefmt),
        }
        # Merge extra context
        extra: dict[str, Any] = getattr(record, "_extra", {})
        entry.update(extra)
        return json.dumps(entry, default=str)


class StdlibLogger(Logger):
    """Logger implementation backed by Python's stdlib logging module.

    Args:
        level: Log level string (DEBUG, INFO, WARNING, ERROR).
        stream: Output stream. Defaults to sys.stdout.
        name: Logger name. Defaults to "app".

    """

    def __init__(
        self,
        level: str = "INFO",
        stream: IO[str] | None = None,
        name: str = "app",
        _bound_context: dict[str, Any] | None = None,
    ) -> None:
        """Build a dedicated stdlib logger with a JSON handler on the given stream.

        A per-instance logger name (suffixed with ``id(self)``) keeps the
        handler isolated from the root logger and from other instances. Existing
        handlers are cleared and propagation disabled so every record is emitted
        exactly once as JSON.

        Args:
            level: Log level string (DEBUG, INFO, WARNING, ERROR).
            stream: Output stream. Defaults to ``sys.stdout``.
            name: Logger name. Defaults to ``"app"``.
            _bound_context: Internal — context carried over from ``bind()``.

        """
        self._level = level
        self._stream: TextIO = cast(TextIO, stream or sys.stdout)
        self._name = name
        self._bound_context: dict[str, Any] = _bound_context or {}

        # Create a dedicated logger to avoid polluting the root logger
        self._logger = logging.getLogger(f"stdlib.{name}.{id(self)}")
        self._logger.setLevel(getattr(logging, level.upper(), logging.INFO))
        self._logger.propagate = False

        # Clear any existing handlers and add our JSON handler
        self._logger.handlers.clear()
        handler = logging.StreamHandler(self._stream)
        handler.setFormatter(_JSONFormatter())
        self._logger.addHandler(handler)

    def debug(self, msg: str, **kwargs: object) -> None:
        """Log at DEBUG level."""
        self._log(logging.DEBUG, msg, **kwargs)

    def info(self, msg: str, **kwargs: object) -> None:
        """Log at INFO level."""
        self._log(logging.INFO, msg, **kwargs)

    def warning(self, msg: str, **kwargs: object) -> None:
        """Log at WARNING level."""
        self._log(logging.WARNING, msg, **kwargs)

    def error(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level."""
        self._log(logging.ERROR, msg, **kwargs)

    def exception(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level with exception info attached."""
        self._log(logging.ERROR, msg, **kwargs)

    def bind(self, **kwargs: object) -> Logger:
        """Return a new StdlibLogger with merged bound context."""
        merged = {**self._bound_context, **kwargs}
        return StdlibLogger(
            level=self._level,
            stream=self._stream,
            name=self._name,
            _bound_context=merged,
        )

    def _log(self, level: int, msg: str, **kwargs: object) -> None:
        """Merge bound context with per-call kwargs and emit a log record."""
        if not self._logger.isEnabledFor(level):
            return
        extra = {**self._bound_context, **kwargs}
        record = self._logger.makeRecord(
            name=self._logger.name,
            level=level,
            fn="",
            lno=0,
            msg=msg,
            args=(),
            exc_info=None,
        )
        record._extra = extra  # type: ignore[attr-defined]  # noqa: SLF001
        self._logger.handle(record)
