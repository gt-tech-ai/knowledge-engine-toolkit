"""Tests for the controller layer: base controller and handler decorators."""

from __future__ import annotations

from dataclasses import dataclass

from techai_webutils.controllers.base import BaseController


@dataclass
class SampleRequest:
    """A request DTO the handler-builder tests decode."""

    input: str


@dataclass
class SampleResponse:
    """A response DTO the handler-builder tests encode."""

    output: str


class TestBaseController:
    """Test suite for the BaseController base class."""

    def test_handle_error_returns_structured_dict(self) -> None:
        """Test that handle_error returns a structured error response.

        **Why this test is important:**
          - All controller error responses flow through handle_error
          - The structured dict format is consumed by HTTP serialization middleware
          - Missing or incorrect fields would cause response serialization failures

        **What it tests:**
          - Response contains an 'error' key
          - Error value matches the exception message
        """
        ctrl = BaseController()
        result = ctrl.handle_error(ValueError("something broke"))
        assert "error" in result
        assert result["error"] == "something broke"

    def test_handle_error_includes_error_type(self) -> None:
        """Test that handle_error includes the exception type name.

        **Why this test is important:**
          - The exception type name is used for error classification in monitoring dashboards
          - Clients may use the type field to implement specific error handling strategies
          - Missing type information would make error categorization impossible

        **What it tests:**
          - Response 'type' field equals the exception class name
        """
        ctrl = BaseController()
        result = ctrl.handle_error(RuntimeError("runtime issue"))
        assert result["type"] == "RuntimeError"
