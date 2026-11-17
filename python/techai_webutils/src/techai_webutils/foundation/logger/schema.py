"""The log schemas ``configure_logging`` can emit, and the v2 structlog processors.

v1 is the original output and the default. v2 is the unified field set the Go zap and
stdlib loggers emit under ``logger.SchemaV2`` (ARCHITECTURE.md#logging): a millisecond
UTC timestamp, a ``dir/file.py:line`` caller, ``service`` and ``env`` on every line, a
logged error flattened to its message beside its ``error_code``, a ``stacktrace``
string at error level, and the legacy keys renamed (``duration`` and ``elapsed_seconds``
to ``duration_seconds``, ``grpc_port`` to ``port``).
"""

from __future__ import annotations

import logging
import sys
import traceback
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import TYPE_CHECKING, Final, cast

import structlog

from techai_webutils.core.errors.codes import ErrorCode
from techai_webutils.core.errors.errors import AppError, InvalidInputError

if TYPE_CHECKING:
    from structlog.typing import EventDict, Processor

SCHEMA_V1: Final = "v1"
"""The original output; the default."""

SCHEMA_V2: Final = "v2"
"""The unified cross-language field set."""

_SCHEMAS: Final = frozenset({"", SCHEMA_V1, SCHEMA_V2})
"""Every accepted schema name; "" counts as v1."""

ERROR_KEY: Final = "error"
"""The field a caller logs an error under; under v2 it holds the error's message."""

ERROR_CODE_KEY: Final = "error_code"
"""The field carrying a logged error's code under v2."""

STACKTRACE_KEY: Final = "stacktrace"
"""The field carrying the stack of an error-level line under v2."""

SCHEMA_V2_KEYS: Final = frozenset({
    "caller",
    "service",
    "env",
    ERROR_CODE_KEY,
    STACKTRACE_KEY,
})
"""Fields the v2 processors stamp; PII redaction skips them, as in Go."""

_ERROR_LEVELS: Final = frozenset({"error", "critical", "exception"})
"""Level names that carry a stacktrace under v2."""

_IGNORED_FRAMES: Final = (
    f"{Path(structlog.__file__).parent}",
    f"{Path(logging.__file__).parent}",
    f"{Path(__file__).parent}",
)
"""Directories whose frames are the logging machinery, not the call site."""

_LEVEL_NAMES: Final = {"warning": "warn", "critical": "error"}
"""structlog level names v2 renames to the Go loggers' names."""

DURATION_KEY: Final = "duration_seconds"
"""The v2 field for a length of time, in seconds."""

PORT_KEY: Final = "port"
"""The v2 field for a listener's port."""

_LEGACY_KEYS: Final = {
    "duration": DURATION_KEY,
    "elapsed_seconds": DURATION_KEY,
    "grpc_port": PORT_KEY,
}
"""v1 field names v2 renames to the unified ones the Go loggers emit."""


@dataclass(frozen=True, slots=True)
class LogSchema:
    """The log schema ``configure_logging`` emits, and the v2 identity fields.

    Construction fails on an unknown schema name, so a typo in ``logging_schema`` fails
    boot as the Go logger's ``NewFromConfig`` does.
    """

    name: str = SCHEMA_V1
    """The schema: "v1" (the default; "" counts as v1) or "v2"."""

    service: str = ""
    """The emitting service, stamped on every v2 line; empty leaves it out."""

    env: str = ""
    """The deploy environment, stamped on every v2 line; empty leaves it out."""

    def __post_init__(self) -> None:
        """Fail on a schema name the logger does not know.

        Raises:
            InvalidInputError: ``name`` is not "", "v1" or "v2".

        """
        if self.name not in _SCHEMAS:
            msg = f"unknown logger schema: {self.name!r}"
            raise InvalidInputError(msg)

    @property
    def is_v2(self) -> bool:
        """Whether this is the v2 field set."""
        return self.name == SCHEMA_V2


def error_message(err: BaseException) -> str:
    """Render an error as Go's ``err.Error()`` would: the message, then its cause.

    Args:
        err: The logged error.

    Returns:
        ``"<message>: <cause>"`` for an AppError with a cause, else ``str(err)``.

    """
    if isinstance(err, AppError):
        return f"{err.message}: {err.cause}" if err.cause else err.message
    return str(err)


def schema_processors(schema: LogSchema) -> list[Processor]:
    """Return the processors that give an event ``schema``'s field set.

    Under v1 that is the original ISO timestamp alone; under v2 they turn the event
    into the unified field set. They run after the level is added and before
    redaction and rendering.

    Args:
        schema: The schema and its identity fields.

    Returns:
        The processors, in order.

    """
    if not schema.is_v2:
        return [structlog.processors.TimeStamper(fmt="iso", key="timestamp")]
    static = {k: v for k, v in (("service", schema.service), ("env", schema.env)) if v}

    def add_static(
        _logger: object, _method_name: str, event_dict: EventDict
    ) -> EventDict:
        """Stamp service and env.

        Args:
            _logger: Ignored (structlog's logger).
            _method_name: Ignored (the log method's name).
            event_dict: The event being logged.

        Returns:
            The event dict.

        """
        event_dict.update(static)
        return event_dict

    return [
        _level_v2,
        _timestamp_v2,
        add_static,
        _rename_legacy_keys,
        _flatten_error,
        _add_caller,
        _add_stacktrace,
    ]


def _level_v2(_logger: object, _method_name: str, event_dict: EventDict) -> EventDict:
    """Rename structlog's ``warning`` and ``critical`` to Go's ``warn`` and ``error``.

    Args:
        _logger: Ignored (structlog's logger).
        _method_name: Ignored (the log method's name).
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    level = cast("object", event_dict.get("level"))
    if isinstance(level, str) and level in _LEVEL_NAMES:
        event_dict["level"] = _LEVEL_NAMES[level]
    return event_dict


def _rename_legacy_keys(
    _logger: object, _method_name: str, event_dict: EventDict
) -> EventDict:
    """Rename ``duration``, ``elapsed_seconds`` and ``grpc_port`` to their v2 fields.

    Args:
        _logger: Ignored (structlog's logger).
        _method_name: Ignored (the log method's name).
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    for legacy, key in _LEGACY_KEYS.items():
        if legacy in event_dict:
            event_dict[key] = event_dict.pop(legacy)
    return event_dict


def _timestamp_v2(_logger: object, _method_name: str, event_dict: EventDict) -> EventDict:
    """Stamp an RFC 3339 UTC timestamp with millisecond precision.

    Args:
        _logger: Ignored (structlog's logger).
        _method_name: Ignored (the log method's name).
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    now = datetime.now(UTC).isoformat(timespec="milliseconds")
    event_dict["timestamp"] = now.replace("+00:00", "Z")
    return event_dict


def _flatten_error(
    _logger: object, _method_name: str, event_dict: EventDict
) -> EventDict:
    """Flatten a logged exception to its message under ``error`` and its ``error_code``.

    The exception is the one under ``error``, else the one ``exc_info`` names (a
    ``logger.exception`` call or a stdlib record), whose ``exc_info`` is consumed so
    no nested "exception" object is rendered. An uncoded exception gets
    ``UNKNOWN``, as Go's ``apperr.Code`` gives it.

    Args:
        _logger: Ignored (structlog's logger).
        _method_name: Ignored (the log method's name).
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    err: object = event_dict.get(ERROR_KEY)
    if not isinstance(err, BaseException):
        err = _exc_info_error(cast("object", event_dict.pop("exc_info", None)))
    if err is None:
        return event_dict
    event_dict[ERROR_KEY] = error_message(err)
    code = err.code if isinstance(err, AppError) else ErrorCode.UNKNOWN
    event_dict[ERROR_CODE_KEY] = code.value
    if err.__traceback__ is not None:
        event_dict["_error_traceback"] = "".join(traceback.format_exception(err))
    return event_dict


def _exc_info_error(exc_info: object) -> BaseException | None:
    """Return the exception an ``exc_info`` value names, as structlog reads it.

    Args:
        exc_info: True (the exception being handled), an exception, or a
            ``sys.exc_info()`` tuple.

    Returns:
        The exception, or None when there is none.

    """
    if exc_info is True:
        return sys.exception()
    if isinstance(exc_info, BaseException):
        return exc_info
    if isinstance(exc_info, tuple):
        found = cast("tuple[object, ...]", exc_info)
        if len(found) > 1 and isinstance(found[1], BaseException):
            return found[1]
    return None


def _app_frames() -> list[traceback.FrameSummary]:
    """Return the current stack without the logging machinery's frames.

    Returns:
        The frames, outermost first; the last is the call site.

    """
    return [
        frame
        for frame in traceback.extract_stack()
        if not frame.filename.startswith(_IGNORED_FRAMES)
    ]


def _add_caller(_logger: object, _method_name: str, event_dict: EventDict) -> EventDict:
    """Stamp the call site as ``dir/file.py:line``, the Go caller's form.

    Args:
        _logger: Ignored (structlog's logger).
        _method_name: Ignored (the log method's name).
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    frames = _app_frames()
    if frames:
        site = Path(frames[-1].filename)
        event_dict["caller"] = f"{site.parent.name}/{site.name}:{frames[-1].lineno}"
    return event_dict


def _add_stacktrace(
    _logger: object, method_name: str, event_dict: EventDict
) -> EventDict:
    """Stamp a stacktrace string on an error-level line.

    It is the logged error's own traceback when it has one, else the call site's
    stack, matching the Go loggers' origin-stack-first rule.

    Args:
        _logger: Ignored (structlog's logger).
        method_name: The log method's name, which carries the level.
        event_dict: The event being logged.

    Returns:
        The event dict.

    """
    origin = cast("object", event_dict.pop("_error_traceback", None))
    if method_name not in _ERROR_LEVELS:
        return event_dict
    if isinstance(origin, str):
        event_dict[STACKTRACE_KEY] = origin
    else:
        event_dict[STACKTRACE_KEY] = "".join(traceback.format_list(_app_frames()))
    return event_dict
