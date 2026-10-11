"""Logger implementations and builder.

Provides StructlogLogger, StdlibLogger, and a builder factory for
config-driven logger selection.
"""

from techai_webutils.foundation.logger.builder import (
    LoggerConfig,
    LoggerKind,
    default_config,
    new_logger_from_config,
)
from techai_webutils.foundation.logger.logger import (
    StructlogLogger,
    configure_logging,
    get_logger,
    new_logger,
)
from techai_webutils.foundation.logger.redact import redact_pii
from techai_webutils.foundation.logger.stdlib_logger import StdlibLogger

__all__ = [
    "LoggerConfig",
    "LoggerKind",
    "StdlibLogger",
    "StructlogLogger",
    "configure_logging",
    "default_config",
    "get_logger",
    "new_logger",
    "new_logger_from_config",
    "redact_pii",
]
