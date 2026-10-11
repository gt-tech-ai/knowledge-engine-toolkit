"""Base controller for shared handler utilities.

Provides BaseController with error formatting and response helpers.
Mirrors Go's ``go/transport/rest/controller.go``.
"""

from __future__ import annotations

from dataclasses import dataclass

from techai_webutils.core.errors.errors import AppError, ErrorCode


@dataclass
class ErrorResponse:
    """HTTP error response body."""

    error: str
    """Human-readable error message returned to the client."""
    code: str = ""
    """Machine-readable error code (the AppError code value; empty for a non-AppError)."""
    details: str = ""
    """Extra error context; stays empty for internal errors whose message is hidden."""


# Error codes whose messages should not be exposed to clients
_INTERNAL_CODES = frozenset({ErrorCode.INTERNAL, ErrorCode.UNKNOWN})
"""Error codes whose messages are hidden from clients (only a generic message is returned).

Mirrors Go's ``WriteError`` in ``go/transport/rest/controller.go``, which hides the message for
``CodeInternal`` and ``CodeUnknown`` only; every other code (INGESTION_ERROR, QUALITY_FAILED,
UPSTREAM, NOT_FOUND, ...) exposes its message as both ``error`` and ``details``.
"""


def map_error_to_http_status(err: Exception) -> int:
    """Map an exception to an HTTP status code.

    AppError instances use their built-in http_status mapping.
    Unknown exceptions default to 500.
    """
    if isinstance(err, AppError):
        return err.http_status
    return 500


class BaseController:
    """Base class providing shared controller utilities.

    Concrete controllers inherit from this to get consistent error formatting
    and response helpers. This class is transport-agnostic -- it does not depend
    on FastAPI, gRPC, or any specific framework.
    """

    @staticmethod
    def handle_error(exc: Exception) -> dict[str, str]:
        """Format an exception into a structured error response dict.

        Args:
            exc: The exception to format.

        Returns:
            A dict with 'error' (message) and 'type' (exception class name).

        """
        return {
            "error": str(exc),
            "type": type(exc).__name__,
        }

    @staticmethod
    def write_json(status: int, data: object) -> dict[str, object]:
        """Format a JSON response with status code.

        Returns a dict with 'status' and 'data' for framework-agnostic use.
        """
        return {"status": status, "data": data}

    @staticmethod
    def write_error(exc: Exception) -> dict[str, object]:
        """Format an error response.

        AppError instances use their error code and message.
        Internal errors hide their message from clients.
        """
        status = map_error_to_http_status(exc)
        response = ErrorResponse(error="internal server error")

        if isinstance(exc, AppError):
            response.code = exc.code.value
            if exc.code not in _INTERNAL_CODES:
                response.error = exc.message
                response.details = exc.message

        return {
            "status": status,
            "data": {
                "error": response.error,
                "code": response.code,
                "details": response.details,
            },
        }

    def write_success(self, data: object) -> dict[str, object]:
        """Format a 200 OK response."""
        return self.write_json(200, data)

    def write_created(self, data: object) -> dict[str, object]:
        """Format a 201 Created response."""
        return self.write_json(201, data)

    @staticmethod
    def write_no_content() -> dict[str, object]:
        """Format a 204 No Content response."""
        return {"status": 204, "data": None}
