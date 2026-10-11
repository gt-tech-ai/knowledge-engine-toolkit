"""Unit tests for the logger builder (factory + config).

Tests that the builder correctly creates logger instances based on LoggerKind,
validates config, and provides sensible defaults.

# Running Tests

Run with: pytest tests/python/test_foundation/test_logger_builder.py -v
"""

from __future__ import annotations

import pytest

from techai_webutils.foundation.logger.builder import (
    LoggerConfig,
    LoggerKind,
    default_config,
    new_logger_from_config,
)
from techai_webutils.foundation.logger.logger import StructlogLogger
from techai_webutils.foundation.logger.stdlib_logger import StdlibLogger


class TestLoggerBuilder:
    """Test suite for logger builder factory."""

    def test_new_logger_structlog(self) -> None:
        """Test that LoggerKind.STRUCTLOG creates a StructlogLogger.

        **Why this test is important:**
          - Structlog is the default production logger
          - Builder must correctly route STRUCTLOG kind

        **What it tests:**
          - Returned instance is a StructlogLogger
        """
        config = LoggerConfig(kind=LoggerKind.STRUCTLOG)
        logger = new_logger_from_config(config)
        assert isinstance(logger, StructlogLogger)

    def test_new_logger_stdlib(self) -> None:
        """Test that LoggerKind.STDLIB creates a StdlibLogger.

        **Why this test is important:**
          - Stdlib logger is the zero-dependency alternative
          - Builder must correctly route STDLIB kind

        **What it tests:**
          - Returned instance is a StdlibLogger
        """
        config = LoggerConfig(kind=LoggerKind.STDLIB)
        logger = new_logger_from_config(config)
        assert isinstance(logger, StdlibLogger)

    def test_unknown_kind_raises(self) -> None:
        """Test that an unknown kind raises ValueError.

        **Why this test is important:**
          - Config-driven values can contain typos
          - Fail-fast is better than silently using a default

        **What it tests:**
          - ValueError is raised for an invalid kind
        """
        config = LoggerConfig(kind="unknown")  # type: ignore[arg-type]
        with pytest.raises(ValueError, match="unknown"):
            new_logger_from_config(config)

    def test_default_config(self) -> None:
        """Test that default_config returns STRUCTLOG kind with INFO level.

        **Why this test is important:**
          - Sensible defaults enable zero-config startup
          - Structlog + INFO is the production standard

        **What it tests:**
          - kind is LoggerKind.STRUCTLOG
          - level is "INFO"
        """
        config = default_config()
        assert config.kind == LoggerKind.STRUCTLOG
        assert config.level == "INFO"
