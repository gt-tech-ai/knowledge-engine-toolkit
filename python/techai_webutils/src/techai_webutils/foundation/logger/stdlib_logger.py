"""Stdlib logging implementation with JSON output.

Uses Python's built-in logging module with a JSON formatter. No external
dependencies (no structlog). Suitable for lightweight services or environments
where structlog is not available.
"""

from __future__ import annotations

import json
import logging
import sys
from typing import IO, TextIO, cast, final, override

from techai_webutils.core.interfaces.logger import Logger


class _JSONFormatter(logging.Formatter):
    """Formats log records as single-line JSON objects."""

    @override
    def format(self, record: logging.LogRecord) -> str:
        """Render a log record as a single-line JSON string.

        Emits the canonical cross-service fields (message, level, logger,
        timestamp) and merges any bound context stashed on the record's
        ``_extra`` attribute. Non-serialisable values fall back to ``str``.

        Args:
            record: The log record.

        Returns:
            A log record as a single-line JSON string.

        """
        entry: dict[str, object] = {
            "message": record.getMessage(),
            "level": record.levelname,
            "logger": record.name,
            "timestamp": self.formatTime(record, self.datefmt),
        }
        # Merge extra context
        extra = cast("dict[str, object]", getattr(record, "_extra", {}))
        entry.update(extra)
        return json.dumps(entry, default=str)


@final
class StdlibLogger(Logger):
    """Logger implementation backed by Python's stdlib logging module."""

    def __init__(
        self,
        level: str = "INFO",
        stream: IO[str] | None = None,
        name: str = "app",
        _bound_context: dict[str, object] | None = None,
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
        self._stream: TextIO = cast("TextIO", stream or sys.stdout)
        self._name = name
        self._bound_context: dict[str, object] = _bound_context or {}

        # Create a dedicated logger to avoid polluting the root logger
        self._logger = logging.getLogger(f"stdlib.{name}.{id(self)}")
        self._logger.setLevel(getattr(logging, level.upper(), logging.INFO))
        self._logger.propagate = False

        # Clear any existing handlers and add our JSON handler
        self._logger.handlers.clear()
        handler = logging.StreamHandler(self._stream)
        handler.setFormatter(_JSONFormatter())
        self._logger.addHandler(handler)

    @override
    def debug(self, msg: str, **kwargs: object) -> None:
        """Log at DEBUG level.

        Args:
            msg: The event, as a short constant message.
            **kwargs: Structured context logged as fields.

        """
        self._log(logging.DEBUG, msg, **kwargs)

    @override
    def info(self, msg: str, **kwargs: object) -> None:
        """Log at INFO level.

        Args:
            msg: The event, as a short constant message.
            **kwargs: Structured context logged as fields.

        """
        self._log(logging.INFO, msg, **kwargs)

    @override
    def warning(self, msg: str, **kwargs: object) -> None:
        """Log at WARNING level.

        Args:
            msg: The event, as a short constant message.
            **kwargs: Structured context logged as fields.

        """
        self._log(logging.WARNING, msg, **kwargs)

    @override
    def error(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level.

        Args:
            msg: The event, as a short constant message.
            **kwargs: Structured context logged as fields.

        """
        self._log(logging.ERROR, msg, **kwargs)

    @override
    def exception(self, msg: str, **kwargs: object) -> None:
        """Log at ERROR level with exception info attached.

        Args:
            msg: The event, as a short constant message.
            **kwargs: Structured context logged as fields.

        """
        self._log(logging.ERROR, msg, **kwargs)

    @override
    def bind(self, **kwargs: object) -> Logger:
        """Return a new StdlibLogger with merged bound context.

        Args:
            **kwargs: Fields every record from the returned logger carries.

        Returns:
            A new StdlibLogger with merged bound context.

        """
        merged = {**self._bound_context, **kwargs}
        return StdlibLogger(
            level=self._level,
            stream=self._stream,
            name=self._name,
            _bound_context=merged,
        )

    def _log(self, level: int, msg: str, **kwargs: object) -> None:
        """Merge bound context with per-call kwargs and emit a log record.

        Args:
            level: The logging level.
            msg: The message.
            **kwargs: The call's fields.

        """
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
        record.__dict__["_extra"] = extra
        self._logger.handle(record)
