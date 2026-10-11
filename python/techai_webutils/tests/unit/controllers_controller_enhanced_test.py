"""Tests for enhanced BaseController, ErrorResponse, and authorization decorator."""

import asyncio

import pytest

from techai_webutils.controllers.base import (
    BaseController,
    ErrorResponse,
    map_error_to_http_status,
)
from techai_webutils.controllers.decorators import HandlerBuilder
from techai_webutils.core.errors.errors import (
    AppError,
    ConflictError,
    ErrorCode,
    ForbiddenError,
    InternalError,
    InvalidInputError,
    NotFoundError,
    UnauthorizedError,
)


class TestMapErrorToHTTPStatus:
    """Test suite for application error to HTTP status code mapping."""

    def test_not_found(self) -> None:
        """Test that NotFoundError maps to HTTP 404.

        **Why this test is important:**
          - NotFoundError is the most common error type in REST APIs
          - Incorrect status code mapping would confuse clients and break error handling contracts
          - HTTP 404 is the universally expected response for missing resources

        **What it tests:**
          - NotFoundError produces status code 404
        """
        assert map_error_to_http_status(NotFoundError("x")) == 404

    def test_invalid_input(self) -> None:
        """Test that InvalidInputError maps to HTTP 400.

        **Why this test is important:**
          - Invalid input errors tell clients their request is malformed and needs correction
          - HTTP 400 signals a client error, preventing unnecessary retries
          - Incorrect mapping would cause clients to treat input errors as server failures

        **What it tests:**
          - InvalidInputError produces status code 400
        """
        assert map_error_to_http_status(InvalidInputError("x")) == 400

    def test_unauthorized(self) -> None:
        """Test that UnauthorizedError maps to HTTP 401.

        **Why this test is important:**
          - Authentication failures must return 401 to trigger client re-authentication flows
          - Incorrect mapping (e.g., 403) would prevent clients from requesting new credentials
          - HTTP 401 vs 403 distinction is critical for security middleware behavior

        **What it tests:**
          - UnauthorizedError produces status code 401
        """
        assert map_error_to_http_status(UnauthorizedError()) == 401

    def test_forbidden(self) -> None:
        """Test that ForbiddenError maps to HTTP 403.

        **Why this test is important:**
          - Authorization failures must return 403 to indicate insufficient permissions
          - Clients use 403 to display access-denied messages rather than re-authentication prompts
          - Correct 401/403 distinction is required for proper security UX

        **What it tests:**
          - ForbiddenError produces status code 403
        """
        assert map_error_to_http_status(ForbiddenError()) == 403

    def test_conflict(self) -> None:
        """Test that ConflictError maps to HTTP 409.

        **Why this test is important:**
          - Conflict errors indicate duplicate or concurrent modification issues
          - HTTP 409 enables clients to implement optimistic concurrency control
          - Incorrect mapping would hide concurrency issues behind generic error responses

        **What it tests:**
          - ConflictError produces status code 409
        """
        assert map_error_to_http_status(ConflictError("x")) == 409

    def test_internal(self) -> None:
        """Test that InternalError maps to HTTP 500.

        **Why this test is important:**
          - Internal errors represent server-side failures that clients cannot fix
          - HTTP 500 signals that retrying may help after a brief delay
          - Correct mapping ensures monitoring tools can distinguish client from server errors

        **What it tests:**
          - InternalError produces status code 500
        """
        assert map_error_to_http_status(InternalError()) == 500

    def test_unknown_exception(self) -> None:
        """Test that unrecognized exceptions default to HTTP 500.

        **Why this test is important:**
          - Unknown exceptions must never leak as unhandled errors
          - Defaulting to 500 provides a safe fallback for any unclassified exception
          - Clients and monitoring tools can still detect and alert on these errors

        **What it tests:**
          - RuntimeError produces status code 500
        """
        assert map_error_to_http_status(RuntimeError("x")) == 500


class TestErrorResponse:
    """Test suite for ErrorResponse dataclass construction and defaults."""

    def test_construction(self) -> None:
        """Test that ErrorResponse initializes with provided and default field values.

        **Why this test is important:**
          - ErrorResponse is the standard error payload for all API responses
          - Incorrect field defaults would cause inconsistent error formats across endpoints
          - The empty string default for details prevents None serialization issues

        **What it tests:**
          - error field is set to the provided value
          - code field is set to the provided value
          - details field defaults to empty string
        """
        resp = ErrorResponse(error="not found", code="NOT_FOUND")
        assert resp.error == "not found"
        assert resp.code == "NOT_FOUND"
        assert not resp.details


class TestBaseControllerEnhanced:
    """Test suite for BaseController response helpers producing structured HTTP responses."""

    def test_write_json(self) -> None:
        """Test that write_json returns a response dict with the given status and data.

        **Why this test is important:**
          - write_json is the foundation for all controller response methods
          - Incorrect structure would break HTTP response serialization middleware
          - Both status and data must be present for the response framework to function

        **What it tests:**
          - Response status matches the provided code
          - Response data matches the provided payload
        """
        ctrl = BaseController()
        result = ctrl.write_json(200, {"key": "value"})
        assert result["status"] == 200
        assert result["data"] == {"key": "value"}

    def test_write_success(self) -> None:
        """Test that write_success returns HTTP 200.

        **Why this test is important:**
          - write_success is the standard response for successful read operations
          - HTTP 200 is the expected status for GET and successful POST responses
          - Incorrect status would confuse clients about operation outcomes

        **What it tests:**
          - Response status is 200
        """
        ctrl = BaseController()
        result = ctrl.write_success({"items": []})
        assert result["status"] == 200

    def test_write_created(self) -> None:
        """Test that write_created returns HTTP 201.

        **Why this test is important:**
          - write_created distinguishes resource creation from general success
          - HTTP 201 tells clients a new resource was successfully created
          - REST API compliance requires 201 for POST operations that create resources

        **What it tests:**
          - Response status is 201
        """
        ctrl = BaseController()
        result = ctrl.write_created({"id": "123"})
        assert result["status"] == 201

    def test_write_no_content(self) -> None:
        """Test that write_no_content returns HTTP 204 with no data.

        **Why this test is important:**
          - write_no_content is the standard response for successful DELETE operations
          - HTTP 204 with null data prevents clients from attempting to parse a response body
          - Incorrect status or non-null data would violate the HTTP specification

        **What it tests:**
          - Response status is 204
          - Response data is None
        """
        ctrl = BaseController()
        result = ctrl.write_no_content()
        assert result["status"] == 204
        assert result["data"] is None

    def test_write_error_not_found(self) -> None:
        """Test that write_error formats NotFoundError with HTTP 404 and error details.

        **Why this test is important:**
          - write_error must correctly map application errors to HTTP responses
          - The error message and code must be included for client-side error handling
          - NotFoundError is the most frequently returned error type in the API

        **What it tests:**
          - Response status is 404
          - Error message matches the exception message
          - Error code is 'NOT_FOUND'
        """
        ctrl = BaseController()
        result = ctrl.write_error(NotFoundError("user not found"))
        assert result["status"] == 404
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "user not found"
        assert data["code"] == "NOT_FOUND"

    def test_write_error_internal_hides_message(self) -> None:
        """Test that InternalError responses hide sensitive details from clients.

        **Why this test is important:**
          - Internal error messages may contain database queries, stack traces, or credentials
          - Leaking internal details is a security vulnerability (CWE-209)
          - The generic message prevents information disclosure while still signaling a server error

        **What it tests:**
          - Response status is 500
          - Error message is generic 'internal server error'
          - Details field is empty to avoid leaking internal information
        """
        ctrl = BaseController()
        result = ctrl.write_error(InternalError("secret db error"))
        assert result["status"] == 500
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "internal server error"
        assert not data["details"]

    def test_write_error_unknown_exception(self) -> None:
        """Test that unknown exceptions are treated as internal errors.

        **Why this test is important:**
          - Unknown exceptions must never leak raw error messages to clients
          - The fallback to internal error ensures consistent security posture
          - All unclassified exceptions are treated as server errors for safety

        **What it tests:**
          - Response status is 500
          - Error message is generic 'internal server error'
        """
        ctrl = BaseController()
        result = ctrl.write_error(RuntimeError("unexpected"))
        assert result["status"] == 500
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "internal server error"

    def test_write_error_unknown_code_hides_message(self) -> None:
        """Test that an UNKNOWN-coded AppError hides its message from clients.

        **Why this test is important:**
          - Go's WriteError hides the message for BOTH CodeInternal and CodeUnknown; an
            UNKNOWN-coded error has no client-safe message and may carry raw internal text
          - Exposing it would be an information-disclosure leak (CWE-209) -- the release blocker
            this test guards against

        **What it tests:**
          - Response status is 500
          - Error message is the generic 'internal server error', NOT the raw "raw db text"
          - Details field is empty
        """
        ctrl = BaseController()
        result = ctrl.write_error(AppError(ErrorCode.UNKNOWN, "raw db text"))
        assert result["status"] == 500
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "internal server error"
        assert "raw db text" not in data["error"]
        assert not data["details"]

    def test_write_error_ingestion_exposes_message(self) -> None:
        """Test that an INGESTION_ERROR exposes its message (matching Go's WriteError).

        **Why this test is important:**
          - Go's WriteError exposes the message for every code except Internal/Unknown, so
            INGESTION_ERROR details reach the client; the Python edge must match Go exactly
          - Hiding it here would diverge from Go and drop actionable pipeline context

        **What it tests:**
          - Response status is 500
          - The message "parser detail" IS present in both error and details
          - Error code is 'INGESTION_ERROR'
        """
        ctrl = BaseController()
        result = ctrl.write_error(AppError(ErrorCode.INGESTION_ERROR, "parser detail"))
        assert result["status"] == 500
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "parser detail"
        assert data["details"] == "parser detail"
        assert data["code"] == "INGESTION_ERROR"

    def test_write_error_quality_failed_exposes_message(self) -> None:
        """Test that a QUALITY_FAILED error exposes its message (matching Go's WriteError).

        **Why this test is important:**
          - QUALITY_FAILED is not an Internal/Unknown code, so Go's WriteError exposes its message
          - The client needs the gate detail to understand why the request was rejected

        **What it tests:**
          - The message "gate detail" IS present in both error and details
          - Error code is 'QUALITY_FAILED'
        """
        ctrl = BaseController()
        result = ctrl.write_error(AppError(ErrorCode.QUALITY_FAILED, "gate detail"))
        data = result["data"]
        assert isinstance(data, dict)
        assert data["error"] == "gate detail"
        assert data["details"] == "gate detail"
        assert data["code"] == "QUALITY_FAILED"


class TestHandlerBuilderAuthorization:
    """Test suite for HandlerBuilder authorization decorator gating handler execution."""

    @pytest.mark.asyncio
    async def test_with_authorization_allowed(self) -> None:
        """Test that authorized requests pass through to the handler.

        **Why this test is important:**
          - Authorization is the primary security gate for all handler endpoints
          - Authorized requests must reach the handler without modification
          - Incorrect pass-through could silently block legitimate API requests

        **What it tests:**
          - Handler result is returned when authorization passes
        """

        async def handler(req: str) -> str:
            await asyncio.sleep(0)
            return f"ok-{req}"

        async def allow() -> bool:
            await asyncio.sleep(0)
            return True

        h = HandlerBuilder(handler, "test").with_authorization(allow).build()
        result = await h("request")
        assert result == "ok-request"

    @pytest.mark.asyncio
    async def test_with_authorization_denied(self) -> None:
        """Test that unauthorized requests are rejected with ForbiddenError.

        **Why this test is important:**
          - Unauthorized access must be blocked before reaching handler logic
          - ForbiddenError maps to HTTP 403, informing clients their credentials lack permission
          - Failure to deny would be a security vulnerability allowing unauthorized data access

        **What it tests:**
          - ForbiddenError is raised when authorization check returns False
        """

        async def handler(_req: str) -> str:
            await asyncio.sleep(0)
            return "should not reach"

        async def deny() -> bool:
            await asyncio.sleep(0)
            return False

        h = HandlerBuilder(handler, "test").with_authorization(deny).build()
        with pytest.raises(ForbiddenError):
            await h("request")
