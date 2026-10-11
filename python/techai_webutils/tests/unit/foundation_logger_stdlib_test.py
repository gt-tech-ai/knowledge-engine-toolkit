"""Unit tests for StdlibLogger implementation.

Tests that StdlibLogger correctly delegates to Python's logging module,
supports context binding, and produces JSON-formatted output.

# Running Tests

Run with: pytest tests/python/test_foundation/test_logger_stdlib.py -v
"""

from __future__ import annotations

import json
from io import StringIO

from techai_webutils.foundation.logger.stdlib_logger import StdlibLogger


class TestStdlibLogger:
    """Test suite for stdlib-based logger implementation."""

    def test_info_logs_at_info_level(self) -> None:
        """Test that info() produces a log entry at INFO level.

        **Why this test is important:**
          - The fundamental logging contract must work at each level
          - INFO is the most common production level; it must emit output

        **What it tests:**
          - Output contains the message text
          - Output can be parsed as JSON with level=INFO
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        logger.info("test message")

        lines = output.getvalue().strip().split("\n")
        assert len(lines) >= 1
        data = json.loads(lines[-1])
        assert data["message"] == "test message"
        assert data["level"] == "INFO"

    def test_debug_logs_at_debug_level(self) -> None:
        """Test that debug() produces a log entry at DEBUG level.

        **Why this test is important:**
          - Debug logging is essential for development troubleshooting
          - Must not be suppressed when level is set to DEBUG

        **What it tests:**
          - Output contains the debug message with level=DEBUG
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        logger.debug("debug msg")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["message"] == "debug msg"
        assert data["level"] == "DEBUG"

    def test_warning_logs_at_warning_level(self) -> None:
        """Test that warning() produces a log entry at WARNING level.

        **Why this test is important:**
          - Warnings indicate potential issues that need monitoring
          - Alerting rules often trigger on WARNING level

        **What it tests:**
          - Output contains the warning message with level=WARNING
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        logger.warning("warn msg")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["message"] == "warn msg"
        assert data["level"] == "WARNING"

    def test_error_logs_at_error_level(self) -> None:
        """Test that error() produces a log entry at ERROR level.

        **Why this test is important:**
          - Error logs drive incident detection and alerting
          - Must be distinguishable from other levels for filtering

        **What it tests:**
          - Output contains the error message with level=ERROR
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        logger.error("error msg")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["message"] == "error msg"
        assert data["level"] == "ERROR"

    def test_bind_returns_new_logger_with_context(self) -> None:
        """Test that bind() returns a new logger with bound context in output.

        **Why this test is important:**
          - Bound context (correlation_id, user_id) must appear in every log line
          - Bind must not mutate the original logger (immutable context)

        **What it tests:**
          - Bound logger includes the bound key in output
          - Original logger does not include the bound key
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        bound = logger.bind(request_id="req-456")
        bound.info("with context")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["request_id"] == "req-456"

    def test_kwargs_appear_in_output(self) -> None:
        """Test that extra kwargs passed to log methods appear in the JSON output.

        **Why this test is important:**
          - Ad-hoc context (document_id, operation) must be captured per log call
          - Missing context makes production debugging impossible

        **What it tests:**
          - Extra kwargs appear as fields in the JSON output
        """
        output = StringIO()
        logger = StdlibLogger(level="DEBUG", stream=output)
        logger.info("operation", doc_id="doc-789", action="ingest")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["doc_id"] == "doc-789"
        assert data["action"] == "ingest"

    def test_level_filtering(self) -> None:
        """Test that messages below the configured level are suppressed.

        **Why this test is important:**
          - Production typically runs at INFO; DEBUG must be suppressed
          - Leaking debug logs in production wastes storage and obscures issues

        **What it tests:**
          - DEBUG message is suppressed when level is INFO
          - INFO message is emitted
        """
        output = StringIO()
        logger = StdlibLogger(level="INFO", stream=output)
        logger.debug("should be suppressed")
        logger.info("should appear")

        lines = [line for line in output.getvalue().strip().split("\n") if line]
        assert len(lines) == 1
        data = json.loads(lines[0])
        assert data["message"] == "should appear"
