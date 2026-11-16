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
import re
from io import StringIO
from typing import NoReturn, cast

import pytest

import techai_webutils.foundation.logger.logger as mod
from techai_webutils.core.errors.codes import ErrorCode
from techai_webutils.core.errors.errors import AppError
from techai_webutils.foundation.logger.logger import (
    StructlogLogger,
    configure_logging,
    get_logger,
)
from techai_webutils.foundation.logger.schema import LogSchema

_EXPECTED_LINES = 2


def _raise_boom() -> NoReturn:
    """Raise the error whose traceback ``exception`` logs.

    Raises:
        ValueError: Boom.

    """
    msg = "boom"
    raise ValueError(msg)


class TestStructuredLogger:
    """Test suite for structured JSON logger output."""

    @staticmethod
    def test_configure_logging_returns_logger() -> None:
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

    @staticmethod
    def test_logger_produces_json_output() -> None:
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
        _ = configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.info("hello", key="value")

        lines = output.getvalue().strip().split("\n")
        assert len(lines) >= 1
        data = cast("dict[str, object]", json.loads(lines[-1]))
        # Canonical cross-service schema renames structlog's "event" to "message".
        assert data["message"] == "hello"
        assert data["key"] == "value"

    @staticmethod
    def test_logger_includes_correlation_id() -> None:
        """Test that logger includes correlation_id when bound to context.

        **Why this test is important:**
          - Distributed tracing depends on correlation_id to link requests across services
          - Without propagation, debugging multi-service issues becomes impossible
          - Bound context must persist across multiple log calls within a request

        **What it tests:**
          - data["correlation_id"] equals "req-123" in the JSON output
        """
        output = StringIO()
        _ = configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test").bind(correlation_id="req-123")
        logger.info("correlated")

        data = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )
        assert data["correlation_id"] == "req-123"

    @staticmethod
    def test_logger_includes_level() -> None:
        """Test that logger includes the log level string in every JSON entry.

        **Why this test is important:**
          - Log level filtering in production requires the level field to be present
          - Alerting rules trigger on specific levels (error, critical)
          - Dashboard queries filter by level for operational visibility

        **What it tests:**
          - data["level"] equals "warning" for a warning-level log call
        """
        output = StringIO()
        _ = configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.warning("a warning")

        data = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )
        assert data["level"] == "warning"

    @staticmethod
    def test_logger_includes_timestamp() -> None:
        """Test that logger includes a timestamp field in every JSON entry.

        **Why this test is important:**
          - Timestamps are essential for ordering log events and correlating with metrics
          - Time-based queries in log aggregation depend on this field
          - Missing timestamps make incident timeline reconstruction impossible

        **What it tests:**
          - "timestamp" key is present in the JSON output
        """
        output = StringIO()
        _ = configure_logging(level="DEBUG", stream=output)
        logger = get_logger("test")
        logger.info("timestamped")

        data = cast("list[object]", json.loads(output.getvalue().strip().split("\n")[-1]))
        assert "timestamp" in data

    @staticmethod
    def test_configure_logging_silences_noisy_libraries() -> None:
        """Test that configure_logging holds noisy AWS/HTTP libraries at WARNING.

        It does so even at app DEBUG.

        **Why this test is important:**
          - At the app's DEBUG level, aiobotocore/botocore emit the full SigV4 signing
            details on every AWS call (SQS/S3), which buries a document's own parse/index
            log lines and makes operational debugging of the ingestion pipeline much
            harder.
          - Operators must be able to read the application's structured logs without them
            being drowned.

        **What it tests:**
          - After configure_logging(level="DEBUG"), the
            botocore/aiobotocore/urllib3/s3transfer AWS loggers AND the
            httpx/httpcore/grpc HTTP+RPC loggers report an effective level of WARNING, so
            their DEBUG/INFO records (raw connect/send/receive frames, per-request lines)
            are suppressed instead of leaking as unstructured text around the structlog
            JSON.
          - The application's own loggers are NOT over-suppressed — an app-namespace
            logger still reports the requested DEBUG level, so the noise-suppression can't
            accidentally mute real app logs.
        """
        _ = configure_logging(level="DEBUG")
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
    """Covers the StructlogLogger level methods and the git_sha field.

    Checked via emitted output.
    """

    @staticmethod
    def test_every_level_and_bind_emit_through_the_inner() -> None:
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
        _ = configure_logging(level="DEBUG", stream=output)
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
        assert [x["level"] for x in cast("list[dict[str, object]]", lines)] == [
            "debug",
            "info",
            "warning",
            "error",
            "error",
            "info",
        ]
        assert [x["message"] for x in cast("list[dict[str, object]]", lines)] == [
            "d",
            "i",
            "w",
            "e",
            "x",
            "bound",
        ]
        assert lines[0]["k"] == 1
        assert lines[-1]["a"] == _EXPECTED_LINES

    @staticmethod
    def test_git_sha_appears_in_output_when_set(monkeypatch: pytest.MonkeyPatch) -> None:
        """A configured GIT_SHA is injected into every emitted log line.

        **Why this test is important:**
          - The git_sha field powers GitHub deep-links from a log line to the exact
            deployed source; without it, incident triage loses that jump. Asserting on
            the emitted line (not the private processor) keeps the test black-box.

        **What it tests:**
          - With GIT_SHA set, an emitted JSON log line carries git_sha with that value.

        Args:
            monkeypatch: Patches the environment for the test.

        """
        # _GIT_SHA is read from the environment at import time, so overriding the
        # already-imported module constant is the seam; the assertion is on output.
        monkeypatch.setattr(mod, "_GIT_SHA", "abc123")

        output = StringIO()
        _ = configure_logging(level="DEBUG", stream=output)
        get_logger("test").info("deployed")

        data = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )
        assert data["git_sha"] == "abc123"


class TestLogSchemaV2:
    """The v2 log schema: the field set every service and language shares."""

    @staticmethod
    def _error_line() -> dict[str, object]:
        """Log one coded error through a v2 logger and return the parsed line.

        Returns:
            The emitted JSON line.

        """
        output = StringIO()
        _ = configure_logging(
            level="INFO",
            stream=output,
            schema=LogSchema(name="v2", service="svc-a", env="env-a"),
        )
        err = AppError(
            ErrorCode.UNAVAILABLE, "store call", cause=ValueError("upstream refused")
        )
        get_logger("test").error("call failed", error=err)
        return cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )

    @staticmethod
    def test_configure_logging_v2_error_line_carries_field_set() -> None:
        """A v2 error line carries the unified field set the Go loggers emit.

        **Why this test is important:**
          - Dashboards, alerts and log queries key on one field set across services
            and languages; a Python line that drifts (a nested error, a microsecond
            timestamp, no service) breaks every query that filters on it.

        **What it tests:**
          - service and env carry the configured values; level is "error".
          - timestamp is RFC 3339 UTC with exactly millisecond precision.
          - error is the message with its cause as a string; error_code is the code.
          - caller is a "dir/file.py:line" string naming this test file; stacktrace is
            a non-empty string.

        """
        line = TestLogSchemaV2._error_line()

        assert line["service"] == "svc-a"
        assert line["env"] == "env-a"
        assert line["level"] == "error"
        assert line["message"] == "call failed"
        assert re.fullmatch(
            r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z", str(line["timestamp"])
        )
        assert line["error"] == "store call: upstream refused"
        assert line["error_code"] == "UNAVAILABLE"
        assert re.fullmatch(r"unit/foundation_logger_test\.py:\d+", str(line["caller"]))
        assert isinstance(line["stacktrace"], str)
        assert line["stacktrace"]

    @staticmethod
    def test_configure_logging_v2_uncoded_error_is_unknown() -> None:
        """A v2 line codes an uncoded exception as UNKNOWN, as Go's apperr.Code does.

        **Why this test is important:**
          - error_code must be present on every error line so a query can group by it;
            a plain exception must not leave the field out.

        **What it tests:**
          - error is the exception's message and error_code is "UNKNOWN".
          - a warning's level is "warn", Go's name, not structlog's "warning".

        """
        output = StringIO()
        _ = configure_logging(level="INFO", stream=output, schema=LogSchema(name="v2"))
        get_logger("test").warning("odd", error=ValueError("bad value"))
        line = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )

        assert line["level"] == "warn"
        assert line["error"] == "bad value"
        assert line["error_code"] == "UNKNOWN"
        assert "stacktrace" not in line
        assert "service" not in line

    @staticmethod
    def test_configure_logging_v2_exc_info_flattens_to_error() -> None:
        """A v2 ``logger.exception`` line carries the error fields, not a nested dict.

        **Why this test is important:**
          - Python services log caught exceptions with ``exc_info``; under v1 that
            renders a nested "exception" object no Go query can read.

        **What it tests:**
          - error is the exception's message, error_code its code, stacktrace its
            traceback naming the raise; no "exception" key remains.

        """

        def call_bedrock() -> None:
            raise AppError(ErrorCode.TIMEOUT, "bedrock call")

        output = StringIO()
        _ = configure_logging(level="INFO", stream=output, schema=LogSchema(name="v2"))
        try:
            call_bedrock()
        except AppError:
            get_logger("test").exception("ingest failed")
        line = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )

        assert line["level"] == "error"
        assert line["error"] == "bedrock call"
        assert line["error_code"] == "TIMEOUT"
        assert "bedrock call" in str(line["stacktrace"])
        assert "exception" not in line

    @staticmethod
    def test_configure_logging_v2_stdlib_line_shares_schema() -> None:
        """A v2 stdlib ``logging`` line from a library renders in the same schema.

        **Why this test is important:**
          - Library lines (botocore, httpx, a framework) printed as bare text are
            unparseable by the log pipeline and carry no service to filter on.

        **What it tests:**
          - A stdlib warning is one JSON line with level "warn", the message, the
            service and a millisecond timestamp.

        """
        output = StringIO()
        _ = configure_logging(
            level="INFO", stream=output, schema=LogSchema(name="v2", service="svc-b")
        )
        logging.getLogger("some.library").warning("pool %s exhausted", "db")
        line = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )

        assert line["level"] == "warn"
        assert line["message"] == "pool db exhausted"
        assert line["service"] == "svc-b"
        assert re.fullmatch(
            r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z", str(line["timestamp"])
        )

    @staticmethod
    def test_configure_logging_v1_default_keeps_shape() -> None:
        """The default schema keeps the v1 shape: no caller, no flattening, no service.

        **Why this test is important:**
          - v2 is opt-in; a consumer that has not switched must see no change.

        **What it tests:**
          - With no schema, the line has no caller, service or error_code field.

        """
        output = StringIO()
        _ = configure_logging(level="INFO", stream=output)
        get_logger("test").info("plain")
        line = cast(
            "dict[str, object]", json.loads(output.getvalue().strip().split("\n")[-1])
        )

        assert "caller" not in line
        assert "service" not in line
        assert "error_code" not in line

    @staticmethod
    def test_log_schema_unknown_name_fails_invalid_input() -> None:
        """An unknown schema name fails INVALID_INPUT rather than logging v1 silently.

        **Why this test is important:**
          - A typo in ``logging_schema`` must fail boot, as the Go logger does.

        **What it tests:**
          - LogSchema(name="v3") raises AppError with code INVALID_INPUT.

        """
        with pytest.raises(AppError) as info:
            _ = LogSchema(name="v3")
        assert info.value.code is ErrorCode.INVALID_INPUT
