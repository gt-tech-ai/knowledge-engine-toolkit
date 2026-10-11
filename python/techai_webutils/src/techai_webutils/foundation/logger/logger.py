"""Structured JSON logging with correlation ID support.

Uses structlog with JSON rendering for machine-readable log output.
Supports correlation ID propagation for request tracing.

new_logger() returns an interfaces.Logger for dependency injection.
configure_logging() returns a raw structlog.BoundLogger for stdlib use.
"""

from __future__ import annotations

import logging
import os
import sys
import threading
from typing import IO, TYPE_CHECKING, TextIO, cast

import structlog
from opentelemetry import trace as otel_trace
from structlog.tracebacks import ExceptionDictTransformer

from techai_webutils.core.interfaces.logger import Logger
from techai_webutils.foundation.logger.redact import redact_pii

if TYPE_CHECKING:
    from structlog.typing import EventDict, WrappedLogger

# Guards the single-configure invariant: new_logger() self-configures logging exactly once
# instead of re-running structlog.configure + logging.basicConfig(force=True) — which tore down and
# re-added the root handlers on every call, a dropped/duplicated-line + off-thread-startup-race risk.
# configure_logging() itself stays un-guarded so setup_observability and the tests can (re)configure
# with an explicit stream/level.
_configure_lock = threading.Lock()
_configured = False


def _add_trace_context(
    _logger: WrappedLogger,
    _method_name: str,
    event_dict: EventDict,
) -> EventDict:
    """Structlog processor that injects the active OTel trace_id/span_id.

    Enables log<->trace correlation in the log and trace backends. No-op when there
    is no active, recording span.
    """
    ctx = otel_trace.get_current_span().get_span_context()
    if ctx.is_valid:
        event_dict["trace_id"] = format(ctx.trace_id, "032x")
        event_dict["span_id"] = format(ctx.span_id, "016x")
    return event_dict


# The one sanctioned bootstrap env read in the logger: GIT_SHA is a deploy-time image constant bound
# at import so every log line carries git_sha (a link from a log line to its source commit). A
# deliberate exception to reading configuration in one place (ARCHITECTURE.md#configuration) rather
# than threading it through every service's settings (mirrors the Go zap logger's GIT_SHA read).
_GIT_SHA = os.environ.get("GIT_SHA", "")
"""Deployed commit SHA (image tag) bound at import; stamped on every log for GitHub deep-links."""


def _add_git_sha(
    _logger: WrappedLogger,
    _method_name: str,
    event_dict: EventDict,
) -> EventDict:
    """Inject the deployed commit (GIT_SHA env = image tag) for GitHub deep-links."""
    if _GIT_SHA:
        event_dict["git_sha"] = _GIT_SHA
    return event_dict


_STRUCTURAL_KEYS = frozenset({"timestamp", "level", "trace_id", "span_id", "git_sha"})
"""Fields the pipeline itself stamps; never redacted (a hex trace id can look like a phone number)."""


def _redact_value(value: object) -> object:
    """Redact every string in ``value``, walking into dict values, lists and tuples.

    Dict keys are kept as they are; a tuple becomes a list (as JSON would render it); every other
    value passes through unchanged.
    """
    if isinstance(value, str):
        return redact_pii(value)
    if isinstance(value, dict):
        return {
            k: _redact_value(v) for k, v in cast("dict[object, object]", value).items()
        }
    if isinstance(value, list | tuple):
        return [
            _redact_value(v) for v in cast("list[object] | tuple[object, ...]", value)
        ]
    return value


def _redact_pii_fields(
    _logger: WrappedLogger,
    _method_name: str,
    event_dict: EventDict,
) -> EventDict:
    """Structlog processor that redacts PII in the message and every field (``redact_pii``).

    Installed only when ``configure_logging(redact_pii=True)`` (the ``logging_redact_pii`` setting).
    Pipeline-stamped structural fields are skipped; strings nested in dicts, lists and tuples are
    redacted too; other values pass through.
    """
    for key, value in event_dict.items():
        if key not in _STRUCTURAL_KEYS:
            event_dict[key] = _redact_value(value)
    return event_dict


class StructlogLogger(Logger):
    """Logger implementation backed by structlog.

    Args:
        inner: A structlog bound logger instance.

    """

    def __init__(self, inner: structlog.stdlib.BoundLogger) -> None:
        """Wrap an already-configured structlog bound logger.

        Args:
            inner: The structlog bound logger that receives the actual log calls.

        """
        self._inner = inner

    def debug(self, msg: str, **kwargs: object) -> None:
        """Log a message at DEBUG level."""
        self._inner.debug(msg, **kwargs)

    def info(self, msg: str, **kwargs: object) -> None:
        """Log a message at INFO level."""
        self._inner.info(msg, **kwargs)

    def warning(self, msg: str, **kwargs: object) -> None:
        """Log a message at WARNING level."""
        self._inner.warning(msg, **kwargs)

    def error(self, msg: str, **kwargs: object) -> None:
        """Log a message at ERROR level."""
        self._inner.error(msg, **kwargs)

    def exception(self, msg: str, **kwargs: object) -> None:
        """Log a message at ERROR level with exception info attached."""
        self._inner.exception(msg, **kwargs)

    def bind(self, **kwargs: object) -> Logger:
        """Return a new logger with the given key-value pairs bound."""
        return StructlogLogger(self._inner.bind(**kwargs))


def new_logger(
    level: str = "INFO",
    stream: IO[str] | None = None,
) -> Logger:
    """Create a Logger backed by structlog.

    This is the preferred factory for dependency injection. To swap the logging
    backend, provide a different implementation of interfaces.Logger.

    Logging is configured exactly once: the FIRST call self-configures structlog (if
    ``setup_observability`` has not already), and every later call only wraps the
    already-configured logger. So ``level``/``stream`` take effect only on that first
    configure — a later ``new_logger(level=...)`` does NOT reconfigure. For explicit
    (re)configuration with a specific level/stream, call ``configure_logging`` or
    ``setup_observability`` directly (both are un-guarded).

    Args:
        level: Log level (DEBUG, INFO, WARNING, ERROR, CRITICAL). Applied only on the
            first (self-)configure; ignored once logging is already configured.
        stream: Output stream. Defaults to sys.stdout. Applied only on the first
            (self-)configure.

    Returns:
        An interfaces.Logger implementation.

    """
    # Configure exactly once (self-configure on first use if setup_observability has not run), then
    # only wrap the already-configured structlog logger — no per-call reconfigure/handler teardown.
    if not _configured:
        with _configure_lock:
            if not _configured:
                configure_logging(level=level, stream=stream)
    return StructlogLogger(structlog.get_logger())


def _parse_level(level: str) -> int:
    """Map a level name (DEBUG/INFO/WARNING/ERROR/CRITICAL) to its logging int.

    An unknown name warns and defaults to INFO rather than silently swallowing a typo
    (the prior getattr(logging, ..., INFO) fallback hid mistakes).
    """
    numeric = getattr(logging, level.upper(), None)
    if not isinstance(numeric, int):
        logging.getLogger(__name__).warning(
            "unknown log level %r; defaulting to INFO", level
        )
        return logging.INFO
    return numeric


def configure_logging(
    level: str = "INFO",
    stream: IO[str] | None = None,
    *,
    redact_pii: bool = False,
) -> structlog.typing.FilteringBoundLogger:
    """Configure structlog for JSON output.

    Prefer new_logger() for DI-friendly code; use this only when you
    need a raw structlog.BoundLogger.

    Args:
        level: Log level (DEBUG, INFO, WARNING, ERROR, CRITICAL).
        stream: Output stream. Defaults to sys.stdout.
        redact_pii: When True (the ``logging_redact_pii`` setting), redact PII in the message and
            every string field before rendering (``foundation.logger.redact.redact_pii``).

    Returns:
        A configured structlog bound logger.

    """
    target_stream: TextIO = cast(TextIO, stream or sys.stdout)
    numeric_level = _parse_level(level)

    redaction = [_redact_pii_fields] if redact_pii else []
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            _add_trace_context,
            _add_git_sha,
            structlog.stdlib.add_log_level,
            structlog.processors.TimeStamper(fmt="iso", key="timestamp"),
            structlog.processors.StackInfoRenderer(),
            # Structured tracebacks WITHOUT frame locals (show_locals=False) under "exception"
            # whenever exc_info is set. dict_tracebacks defaults to dumping every frame's locals,
            # which turned a single Bedrock ClientError into a multi-KB wall of botocore internals
            # (and risks leaking sensitive values); the frames are kept for analysis, locals dropped.
            structlog.processors.ExceptionRenderer(
                ExceptionDictTransformer(show_locals=False)
            ),
            # Canonical cross-service schema: the message lives under "message"
            # (matching the Go zap/stdlib loggers), not structlog's default "event".
            structlog.processors.EventRenamer("message"),
            *redaction,
            structlog.processors.JSONRenderer(),
        ],
        # make_filtering_bound_logger gates the app's own structlog output at the numeric
        # level (dropping below-threshold events before rendering). structlog.stdlib.BoundLogger
        # does NOT filter with a PrintLogger factory, so the level was inert for app logs and
        # every .debug() printed regardless of level.
        wrapper_class=structlog.make_filtering_bound_logger(numeric_level),
        context_class=dict,
        logger_factory=structlog.PrintLoggerFactory(file=target_stream),
        # NOTE: cache_logger_on_first_use=True is intentionally NOT applied. The cached
        # bound logger binds to the PrintLoggerFactory's stream at first use and is NOT rebuilt when
        # configure() is re-run with a different stream, which breaks the per-test stream-capture
        # pattern (test_observability et al.). The per-call bound-logger rebuild is cheap relative to
        # the JSON render + I/O; the configure-once guard already removes the far costlier per-call
        # structlog.configure + basicConfig(force=True) teardown.
        cache_logger_on_first_use=False,
    )

    logging.basicConfig(
        format="%(message)s",
        stream=target_stream,
        level=numeric_level,
        force=True,
    )

    # Pin noisy third-party libraries above the app level. At the app's DEBUG level, aiobotocore/botocore
    # dump the full SigV4 canonical request + StringToSign + signature on EVERY AWS call (SQS receive/
    # delete, S3 get/put), and urllib3/s3transfer are similarly chatty. httpx logs an "HTTP Request:"
    # line per call and httpcore dumps raw connect/send/receive frames (the qdrant + retrieval HTTP
    # traffic), and grpc emits its own event lines — all as UNSTRUCTURED text outside our structlog
    # JSON pipeline, flooding a service's own parse/index lines. None of it carries operational signal
    # for us, so hold these libraries at WARNING regardless of the app level.
    # Note: this also silences grpc.aio's INFO connection diagnostics — remember this pin is why
    # they're quiet if you ever chase a dev-only gRPC connectivity issue.
    for noisy_logger in (
        "botocore",
        "aiobotocore",
        "boto3",
        "boto",
        "s3transfer",
        "urllib3",
        "httpx",
        "httpcore",
        "grpc",
    ):
        logging.getLogger(noisy_logger).setLevel(logging.WARNING)

    # Mark configured so new_logger() self-configures at most once; configure_logging itself
    # stays un-guarded, so setup_observability + the tests may (re)configure with an explicit stream.
    global _configured  # noqa: PLW0603
    _configured = True
    return structlog.get_logger()


def get_logger(name: str, **initial_values: object) -> structlog.stdlib.BoundLogger:
    """Get a named logger with optional initial bound values.

    Args:
        name: Logger name (typically module or service name).
        **initial_values: Key-value pairs to bind to the logger.

    Returns:
        A bound structlog logger.

    """
    logger: structlog.stdlib.BoundLogger = structlog.get_logger(name, **initial_values)
    return logger
