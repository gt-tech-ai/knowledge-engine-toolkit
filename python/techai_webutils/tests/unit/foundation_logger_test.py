"""Unit tests for structured logging.

This file tests that the foundation logger produces machine-readable JSON output
with correct log levels, timestamps, and injected context fields (e.g.,
correlation IDs) for observability and distributed tracing.

# Test Coverage

The tests cover:
  - Logger creation: configure_logging returns a usable logger instance
  - JSON output: logger produces valid JSON with event and key-value fields
  - Context binding: correlation_id propagates through bound context
  - Log level: level string is captured in output
  - Timestamps: every log entry includes a timestamp field

# Test Structure

Tests use pytest class-based organization with StringIO capture for log output
verification. No external services or mocking needed; tests validate the
structured output format directly.

# Running Tests

Run with: pytest tests/python/test_foundation/test_logger.py
"""

import json
import logging
from io import StringIO
from typing import NoReturn

import pytest

from techai_webutils.foundation.logger.logger import (
    StructlogLogger,
    configure_logging,
    get_logger,
)


def _raise_boom() -> NoReturn:
    """Raise the error whose traceback ``exception`` logs."""
    msg = "boom"
    raise ValueError(msg)


class TestStructuredLogger:
    """Test suite for structured JSON logger output."""

    def test_configure_logging_returns_logger(self) -> None:
        """Test that configure_logging returns a non-None bound logger instance.

        **Why this test is important:**
          - Every service depends on having a logger at startup
          - A None return would cause AttributeError on the first log call
          - Configuration must succeed with minimal arguments for development use

        **What it tests:**
          - Return value of configure_logging is not None
        """
        logger = configure_logging(level="DEBUG")
        assert logger is not None

    def test_logger_produces_json_output(self) -> None:
        """Test that logger produces valid JSON with event name and bound key-value pairs.

        **Why this test is important:**
          - Log aggregation systems (ELK, Datadog) require structured JSON for parsing
          - Invalid JSON would cause log ingestion failures and blind spots
          - Key-value pairs must be preserved for filtering and searching in dashboards

        **What it tests:**
          - Output is valid JSON (json.loads succeeds)
          - data["message"] equals the logged event name "hello"
          - data["key"] equals the bound value "value"
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.info("hello", key="value")

        lines = output.getvalue().strip().split("\n")
        assert len(lines) >= 1
        data = json.loads(lines[-1])
        # Canonical cross-service schema renames structlog's "event" to "message".
        assert data["message"] == "hello"
        assert data["key"] == "value"

    def test_logger_includes_correlation_id(self) -> None:
        """Test that logger includes correlation_id when bound to context.

        **Why this test is important:**
          - Distributed tracing depends on correlation_id to link requests across services
          - Without propagation, debugging multi-service issues becomes impossible
          - Bound context must persist across multiple log calls within a request

        **What it tests:**
          - data["correlation_id"] equals "req-123" in the JSON output
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test").bind(correlation_id="req-123")
        logger.info("correlated")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["correlation_id"] == "req-123"

    def test_logger_includes_level(self) -> None:
        """Test that logger includes the log level string in every JSON entry.

        **Why this test is important:**
          - Log level filtering in production requires the level field to be present
          - Alerting rules trigger on specific levels (error, critical)
          - Dashboard queries filter by level for operational visibility

        **What it tests:**
          - data["level"] equals "warning" for a warning-level log call
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.warning("a warning")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["level"] == "warning"

    def test_logger_includes_timestamp(self) -> None:
        """Test that logger includes a timestamp field in every JSON entry.

        **Why this test is important:**
          - Timestamps are essential for ordering log events and correlating with metrics
          - Time-based queries in log aggregation depend on this field
          - Missing timestamps make incident timeline reconstruction impossible

        **What it tests:**
          - "timestamp" key is present in the JSON output
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.info("timestamped")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert "timestamp" in data

    def test_configure_logging_silences_noisy_libraries(self) -> None:
        """Test that configure_logging holds noisy AWS/HTTP libraries at WARNING even at app DEBUG.

        **Why this test is important:**
          - At the app's DEBUG level, aiobotocore/botocore emit the full SigV4 signing details on every
            AWS call (SQS/S3), which buries a document's own parse/index log lines and makes operational
            debugging of the ingestion pipeline much harder.
          - Operators must be able to read the application's structured logs without them being drowned.

        **What it tests:**
          - After configure_logging(level="DEBUG"), the botocore/aiobotocore/urllib3/s3transfer AWS
            loggers AND the httpx/httpcore/grpc HTTP+RPC loggers report an effective level of WARNING,
            so their DEBUG/INFO records (raw connect/send/receive frames, per-request lines) are
            suppressed instead of leaking as unstructured text around the structlog JSON.
          - The application's own loggers are NOT over-suppressed — an app-namespace logger still reports
            the requested DEBUG level, so the noise-suppression can't accidentally mute real app logs.
        """
        configure_logging(level="DEBUG")
        for name in (
            "botocore",
            "aiobotocore",
            "urllib3",
            "s3transfer",
            "httpx",
            "httpcore",
            "grpc",
        ):
            assert logging.getLogger(name).getEffectiveLevel() == logging.WARNING
        assert (
            logging.getLogger("knowledge_engine_ingestion.jobs").getEffectiveLevel()
            == logging.DEBUG
        )


class TestStructlogLoggerDelegation:
    """Covers the StructlogLogger level methods and the git_sha field via emitted output."""

    def test_every_level_and_bind_emit_through_the_inner(self) -> None:
        """Every level method (and bind) produces an emitted line at that level.

        **Why this test is important:**
          - StructlogLogger is the Logger seam every tier logs through; a dropped
            level (e.g. exception) would silently lose error logs in production. This
            asserts the observable output — a real emitted JSON line per level —
            rather than that an inner mock was called, so it survives a refactor of
            the delegation internals.

        **What it tests:**
          - debug/info/warning/error/exception each emit a JSON line at the matching
            level (exception at error), the first line carries its kwarg, and a
            bound field propagates into a later line.
        """
        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        log = StructlogLogger(get_logger("test"))

        log.debug("d", k=1)
        log.info("i")
        log.warning("w")
        log.error("e")
        try:
            _raise_boom()
        except ValueError:
            log.exception("x")
        log.bind(a=2).info("bound")

        lines = [json.loads(line) for line in output.getvalue().strip().split("\n")]
        assert [x["level"] for x in lines] == [
            "debug",
            "info",
            "warning",
            "error",
            "error",
            "info",
        ]
        assert [x["message"] for x in lines] == ["d", "i", "w", "e", "x", "bound"]
        assert lines[0]["k"] == 1
        assert lines[-1]["a"] == 2

    def test_git_sha_appears_in_output_when_set(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        """A configured GIT_SHA is injected into every emitted log line.

        **Why this test is important:**
          - The git_sha field powers GitHub deep-links from a log line to the exact
            deployed source; without it, incident triage loses that jump. Asserting on
            the emitted line (not the private processor) keeps the test black-box.

        **What it tests:**
          - With GIT_SHA set, an emitted JSON log line carries git_sha with that value.
        """
        import techai_webutils.foundation.logger.logger as mod

        # _GIT_SHA is read from the environment at import time, so overriding the
        # already-imported module constant is the seam; the assertion is on output.
        monkeypatch.setattr(mod, "_GIT_SHA", "abc123")

        output = StringIO()
        configure_logging(level="DEBUG", stream=output)
        get_logger("test").info("deployed")

        data = json.loads(output.getvalue().strip().split("\n")[-1])
        assert data["git_sha"] == "abc123"
